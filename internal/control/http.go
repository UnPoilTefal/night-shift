package control

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/relay"
)

// Bornes de l'API des étapes.
const (
	// MaxClaimWait plafonne l'attente longue d'une réclamation.
	MaxClaimWait = 2 * time.Minute
	// maxEventBytes borne un événement, rapport compris.
	maxEventBytes = 4 << 20
)

var eventTypes = []relay.EventType{relay.Started, relay.Finished, relay.Incident, relay.PassFinished}

// StagesHandler sert l'API des étapes de confiance (contrat du relais v1),
// sans authentification propre : seul control-net y mène, et seules les
// étapes de confiance y sont.
func (s *Service) StagesHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+relay.ClaimPath, s.serveClaim)
	mux.HandleFunc("POST /v1/passes/{pass}/events", s.serveEvent)
	return mux
}

func (s *Service) serveClaim(w http.ResponseWriter, r *http.Request) {
	wait, err := time.ParseDuration(r.URL.Query().Get("wait"))
	if err != nil || wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(min(wait, MaxClaimWait))
	defer timer.Stop()
	for {
		c, ok, changed := s.claim()
		if ok {
			writeJSON(w, http.StatusOK, c)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Service) serveEvent(w http.ResponseWriter, r *http.Request) {
	var e relay.Event
	if err := json.NewDecoder(io.LimitReader(r.Body, maxEventBytes)).Decode(&e); err != nil || e.Stage == "" || !slices.Contains(eventTypes, e.Type) {
		http.Error(w, "événement invalide", http.StatusBadRequest)
		return
	}
	res, err := s.event(r.PathValue("pass"), e)
	switch {
	case err != nil:
		http.Error(w, "historique non écrit", http.StatusInternalServerError)
	case res == unknownPass:
		http.Error(w, "passe inconnue", http.StatusNotFound)
	case res == closedPass:
		http.Error(w, "passe déjà close", http.StatusConflict)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminPrefix est le préfixe de l'API d'administration.
const AdminPrefix = "/v1/admin"

// unauthorized est la réponse à un jeton absent ou faux : elle ne cite
// jamais le jeton reçu ni l'attendu.
const unauthorized = "jeton d'administration absent ou invalide"

// AdminHandler sert l'API d'administration, en lecture seule, protégée par
// le jeton (en-tête Authorization: Bearer <jeton>) :
//
//	GET /v1/admin/status       passe active, passe en attente, prochaine échéance
//	GET /v1/admin/passes       historique, de la plus récente à la plus ancienne
//	GET /v1/admin/passes/{id}  historique complet d'une passe
func (s *Service) AdminHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+AdminPrefix+"/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Status())
	})
	mux.HandleFunc("GET "+AdminPrefix+"/passes", s.serveHistory)
	mux.HandleFunc("GET "+AdminPrefix+"/passes/{pass}", s.servePass)
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			http.Error(w, unauthorized, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Summary résume une passe de l'historique.
type Summary struct {
	Pass     string    `json:"pass"`
	Trigger  string    `json:"trigger"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Status   string    `json:"status"`
	// Tickets et Incidents comptent les tickets traités et les incidents
	// (détaillés par les étapes ou comptés par le rapport).
	Tickets   int `json:"tickets"`
	Incidents int `json:"incidents"`
	// URL est l'adresse de son historique complet.
	URL string `json:"url"`
}

func (s *Service) serveHistory(w http.ResponseWriter, _ *http.Request) {
	ids, err := s.store.ids()
	if err != nil {
		http.Error(w, "historique illisible", http.StatusInternalServerError)
		return
	}
	out := []Summary{}
	for _, id := range slices.Backward(ids) {
		r, ok, err := s.store.read(id)
		if err != nil || !ok {
			continue
		}
		sum := Summary{Pass: r.Pass, Trigger: r.Trigger, Started: r.Started, Finished: r.Finished, Status: r.Status,
			URL: AdminPrefix + "/passes/" + url.PathEscape(r.Pass)}
		reported := 0
		for _, e := range r.Events {
			switch {
			case e.Type == relay.Incident:
				sum.Incidents++
			case e.Type == relay.PassFinished && e.Report != nil:
				sum.Tickets = len(e.Report.Tickets)
				reported = len(e.Report.Errors)
			}
		}
		sum.Incidents = max(sum.Incidents, reported)
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) servePass(w http.ResponseWriter, r *http.Request) {
	rec, ok, err := s.store.read(r.PathValue("pass"))
	switch {
	case err != nil:
		http.Error(w, "historique illisible", http.StatusInternalServerError)
	case !ok:
		http.Error(w, "passe inconnue", http.StatusNotFound)
	default:
		writeJSON(w, http.StatusOK, rec)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
