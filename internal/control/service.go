// Package control est le service de contrôle hors Kubernetes (#109) : il
// déclenche les passes à l'échéance ou à la demande, n'en laisse tourner
// qu'une à la fois, sert le relais des étapes de confiance (contrat v1,
// paquet relay) et tient l'historique des passes, qui ne quitte jamais
// l'hôte. Il ne détient aucun jeton de forge ni de modèle et ne fait aucun
// appel sortant. C'est l'adaptateur Compose du plan de contrôle (ADR 0013).
package control

import (
	"cmp"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/verdict"
)

// Valeurs par défaut.
const (
	// DefaultWatchdog dépasse la somme des délais des étapes de l'exemple
	// (deux agents de 30 minutes, deux attentes de CI de 10 minutes), plus
	// une marge.
	DefaultWatchdog = 2 * time.Hour
	// DefaultRetention est la durée de conservation de l'historique et des
	// transcripts.
	DefaultRetention = 30 * 24 * time.Hour
	// purgeEvery est l'intervalle entre deux purges.
	purgeEvery = 24 * time.Hour
)

// Statuts d'une passe close.
const (
	// Finished : une étape a envoyé pass-finished avec le rapport.
	Finished = "finished"
	// Failed : le chien de garde a clos la passe à son échéance.
	Failed = "failed"
)

// Config règle le service.
type Config struct {
	// Schedule est l'échéance des passes ; nil, seules les demandes
	// manuelles en déclenchent.
	Schedule cron.Schedule
	// Watchdog est la durée maximale d'une passe (DefaultWatchdog si nulle).
	Watchdog time.Duration
	// History et Transcripts sont les dossiers de l'historique et de
	// l'archive des transcripts ; Retention, leur durée de conservation
	// (DefaultRetention si nulle). Transcripts peut être vide.
	History, Transcripts string
	Retention            time.Duration
	// Now est l'horloge (time.Now si nulle).
	Now func() time.Time
}

// Record est l'historique d'une passe.
type Record struct {
	Pass      string    `json:"pass"`
	Trigger   string    `json:"trigger"`
	Requested time.Time `json:"requested"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished,omitzero"`
	// Status vaut "" tant que la passe est active, puis Finished ou Failed.
	Status string `json:"status,omitempty"`
	// Reason motive un échec.
	Reason string `json:"reason,omitempty"`
	// Events sont les événements reçus des étapes, incidents détaillés
	// compris ; le dernier pass-finished porte le rapport (tickets, dépôts
	// écartés, incidents).
	Events []relay.Event `json:"events"`
}

// Service est l'état du service de contrôle. Il n'y a jamais plus d'une
// passe active, ni plus d'une passe en attente : les demandes reçues entre
// deux réclamations s'y fondent (ADR 0008).
type Service struct {
	cfg   Config
	store store

	mu      sync.Mutex
	queued  *relay.Claim
	active  *Record
	closed  map[string]bool
	lastID  string
	next    time.Time
	purged  time.Time
	changed chan struct{}
}

// New construit le service et relit l'historique : une passe déjà close le
// reste après un redémarrage.
func New(cfg Config) (*Service, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Watchdog = cmp.Or(cfg.Watchdog, DefaultWatchdog)
	cfg.Retention = cmp.Or(cfg.Retention, DefaultRetention)
	s := &Service{cfg: cfg, store: store{dir: cfg.History}, closed: map[string]bool{}, changed: make(chan struct{})}
	ids, err := s.store.ids()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.closed[id] = true
		s.lastID = max(s.lastID, id)
	}
	if cfg.Schedule != nil {
		s.next = cfg.Schedule.Next(cfg.Now())
	}
	return s, nil
}

// notify réveille les réclamations en attente ; à appeler sous verrou.
func (s *Service) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Request demande une passe : elle rejoint la passe en attente s'il y en a
// une, et n'en ajoute jamais une seconde.
func (s *Service) Request(trigger string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued != nil {
		return
	}
	s.queued = &relay.Claim{Trigger: trigger, Requested: s.cfg.Now().UTC()}
	s.notify()
}

// claim rend la passe en attente et la rend active, si aucune ne l'est ;
// sinon, il rend le canal qui signale un changement.
func (s *Service) claim() (relay.Claim, bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued == nil || s.active != nil {
		return relay.Claim{}, false, s.changed
	}
	c := *s.queued
	s.queued = nil
	now := s.cfg.Now().UTC()
	c.Pass = s.newID(now)
	s.active = &Record{Pass: c.Pass, Trigger: c.Trigger, Requested: c.Requested, Started: now, Events: []relay.Event{}}
	return c, true, nil
}

// newID rend un identifiant de passe au format des étapes, jamais rendu
// auparavant : deux passes dans la même seconde, ou une horloge en retard
// sur l'historique, partent de la seconde qui suit la dernière passe.
func (s *Service) newID(now time.Time) string {
	if last, err := time.Parse("pass-20060102T150405Z", s.lastID); err == nil && !now.After(last) {
		now = last.Add(time.Second)
	}
	for t := now; ; t = t.Add(time.Second) {
		if id := "pass-" + t.Format("20060102T150405Z"); id > s.lastID && !s.closed[id] {
			s.lastID = id
			return id
		}
	}
}

// eventResult est l'issue de la réception d'un événement.
type eventResult int

const (
	accepted eventResult = iota
	unknownPass
	closedPass
)

// event enregistre l'événement d'une étape ; pass-finished clôt la passe.
func (s *Service) event(passID string, e relay.Event) (eventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed[passID]:
		return closedPass, nil
	case s.active == nil || s.active.Pass != passID:
		return unknownPass, nil
	}
	if e.At.IsZero() {
		e.At = s.cfg.Now().UTC()
	}
	s.active.Events = append(s.active.Events, e)
	if e.Type == relay.PassFinished {
		return accepted, s.close(Finished, "")
	}
	return accepted, nil
}

// close clôt la passe active et l'écrit dans l'historique ; à appeler sous
// verrou.
func (s *Service) close(status, reason string) error {
	r := s.active
	r.Status, r.Reason, r.Finished = status, reason, s.cfg.Now().UTC()
	s.closed[r.Pass] = true
	s.active = nil
	s.notify()
	return s.store.write(*r)
}

// Tick fait avancer le service à now : échéance cron, chien de garde et
// purge quotidienne. La boucle de Serve l'appelle chaque seconde ; les tests
// l'appellent avec une horloge simulée.
func (s *Service) Tick(now time.Time) error {
	s.mu.Lock()
	var err error
	if s.active != nil && now.Sub(s.active.Started) >= s.cfg.Watchdog {
		pass := s.active.Pass
		err = s.close(Failed, "durée maximale de la passe dépassée ("+s.cfg.Watchdog.String()+") : aucune étape ne l'a close")
		if s.cfg.Transcripts != "" {
			err = cmp.Or(err, verdict.Append(s.cfg.Transcripts, verdict.Verdict{
				At: now.UTC(), Platform: verdict.Platform, Pass: pass,
				Brick: "B17", Realization: "chien de garde de control (--watchdog)", Rule: s.cfg.Watchdog.String(),
				Result: verdict.Stopped, Effect: true, ActionClass: "passe", WrittenBy: "control",
			}))
		}
	}
	due := s.cfg.Schedule != nil && !now.Before(s.next)
	if due {
		s.next = s.cfg.Schedule.Next(now)
	}
	purge := now.Sub(s.purged) >= purgeEvery
	if purge {
		s.purged = now
	}
	s.mu.Unlock()
	if due {
		s.Request(relay.Schedule)
	}
	if purge {
		err = cmp.Or(err, Purge(now.Add(-s.cfg.Retention), s.cfg.History, s.cfg.Transcripts))
	}
	return err
}

// Status est l'état courant du service.
type Status struct {
	Active *Record      `json:"active"`
	Queued *relay.Claim `json:"queued"`
	// NextSchedule est la prochaine échéance cron, absente sans échéance.
	NextSchedule time.Time `json:"nextSchedule,omitzero"`
}

// Status rend l'état courant.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Status
	if s.active != nil {
		r := *s.active
		r.Events = append([]relay.Event{}, s.active.Events...)
		st.Active = &r
	}
	if s.queued != nil {
		q := *s.queued
		st.Queued = &q
	}
	if s.cfg.Schedule != nil {
		st.NextSchedule = s.next
	}
	return st
}
