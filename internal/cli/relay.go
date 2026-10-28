package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

// Réglages du mode relais, que les tests resserrent.
var (
	// relayPoll est l'intervalle entre deux lectures d'un marqueur.
	relayPoll = time.Second
	// claimWait est l'attente longue d'une réclamation de passe.
	claimWait = time.Minute
	// relayRounds borne le nombre de tours traités avant de rendre la main ;
	// 0, sans fin.
	relayRounds = 0
	// requirePID1 exige que l'agent en relais tourne en PID 1 : killOthers
	// signale tous les processus que l'appelant peut atteindre, ce qui ne se
	// borne au conteneur que si l'agent en est le premier processus.
	requirePID1 = true
	// killOthers tue, après chaque tour de l'agent, tous les autres processus
	// du conteneur.
	killOthers = killAllButSelf
	// stageContext est le contexte d'une étape : il s'annule à l'arrêt du
	// conteneur.
	stageContext = signalContext
	// afterCopy suit la copie de la sortie de l'agent par publish ; les tests
	// y réécrivent la sortie d'origine, comme un agent qui forge sa fin.
	afterCopy = func(stage.Dirs) {}
)

// relayFlags porte les options du mode relais (ADR 0013). Sans --relay, une
// étape fait un seul tour, comme sous le CronJob.
type relayFlags struct {
	on bool
	// control et roundTimeout ne valent que pour les étapes de confiance.
	control      string
	roundTimeout time.Duration
	trusted      bool
}

// deadlineReason ouvre le motif d'un tour dont l'échéance de relais est
// passée ; le journal des verdicts le reconnaît.
const deadlineReason = "échéance du tour"

func addRelayFlags(fs *flag.FlagSet, trusted bool) *relayFlags {
	rf := &relayFlags{trusted: trusted}
	fs.BoolVar(&rf.on, "relay", false, "mode relais : l'étape tourne en service permanent et attend son tour")
	if trusted {
		fs.StringVar(&rf.control, "control", "", "en relais : adresse du service de contrôle, par exemple http://control:8080")
		fs.DurationVar(&rf.roundTimeout, "round-timeout", 30*time.Minute, "en relais : échéance d'un tour de l'agent, plus longue que son --timeout")
	}
	return rf
}

// valid signale les options de relais incohérentes, en erreur d'usage.
func (rf *relayFlags) valid(cmd string, stderr io.Writer) bool {
	msg := ""
	switch {
	case !rf.trusted:
	case rf.on && rf.control == "":
		msg = "--relay exige --control, l'adresse du service de contrôle"
	case !rf.on && rf.control != "":
		msg = "--control ne vaut qu'avec --relay"
	case rf.roundTimeout <= 0:
		msg = "--round-timeout doit être positif"
	}
	if msg != "" {
		_, _ = fmt.Fprintf(stderr, "%s : %s\n", cmd, msg)
	}
	return msg == ""
}

// reporter rapporte les événements d'une étape de confiance au service de
// contrôle. Un échec d'envoi n'arrête jamais l'étape : il est journalisé, et
// le chien de garde du service clora la passe.
type reporter struct {
	client relay.Client
	stage  string
	stderr io.Writer
}

func (r *reporter) send(ctx context.Context, passID string, e relay.Event) {
	e.Stage = r.stage
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if err := r.client.Send(ctx, passID, e); err != nil {
		_, _ = fmt.Fprintf(r.stderr, "%s : événement %s non rapporté au service de contrôle : %v\n", r.stage, e.Type, err)
	}
}

func (r *reporter) incidents(ctx context.Context, passID string, details []string) {
	for _, d := range details {
		r.send(ctx, passID, relay.Event{Type: relay.Incident, Detail: d})
	}
}

func (r *reporter) finished(ctx context.Context, passID string, code int) {
	r.send(ctx, passID, relay.Event{Type: relay.Finished, Exit: &code})
}

// pause attend d, sauf arrêt ; elle rend false à l'arrêt.
func pause(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// more dit si une boucle de relais doit encore traiter un tour.
func more(ctx context.Context, n int) bool {
	return ctx.Err() == nil && (relayRounds == 0 || n < relayRounds)
}

// relaySelect réclame les passes au service de contrôle, l'une après
// l'autre. Pour chacune, il vide les volumes de la passe précédente, fait
// tourner la sélection, puis écrit le marqueur du premier tour, en dernier.
func relaySelect(ctx context.Context, rf *relayFlags, rep *reporter, d stage.Dirs, clean []string,
	once func(ctx context.Context, passID string) (pass.Report, bool, int)) int {
	c := rep.client
	for n := 0; more(ctx, n); {
		cl, ok, err := c.Claim(ctx, claimWait)
		if ctx.Err() != nil {
			return 0
		}
		if err != nil {
			_, _ = fmt.Fprintf(rep.stderr, "select : réclamation d'une passe : %v\n", err)
			if !pause(ctx, relayPoll) {
				return 0
			}
			continue
		}
		if !ok {
			continue
		}
		n++
		rep.send(ctx, cl.Pass, relay.Event{Type: relay.Started})
		if err := clearDirs(clean); err != nil {
			detail := "nettoyage des volumes de la passe : " + err.Error()
			_, _ = fmt.Fprintf(rep.stderr, "select : %s\n", detail)
			rep.incidents(ctx, cl.Pass, []string{detail})
			rep.finished(ctx, cl.Pass, 1)
			continue
		}
		report, reserved, code := once(ctx, cl.Pass)
		turn := relay.Turn{Pass: cl.Pass, Round: 1, Ticket: reserved, Deadline: time.Now().Add(rf.roundTimeout).UTC()}
		if err := relay.WriteTurn(d.State, turn); err != nil {
			report.Errors = append(report.Errors, "écriture du marqueur de tour : "+err.Error())
			_, _ = fmt.Fprintf(rep.stderr, "select : écriture du marqueur de tour : %v\n", err)
			code = 1
		}
		rep.incidents(ctx, cl.Pass, report.Errors)
		rep.finished(ctx, cl.Pass, code)
	}
	return 0
}

// sameTurn dit si deux marqueurs désignent le même tour.
func sameTurn(a, b relay.Turn) bool { return a.Pass == b.Pass && a.Round == b.Round }

// nextTurn attend dans dir un marqueur de tour que l'étape n'a pas encore
// vu ; ok vaut false à l'arrêt.
func nextTurn(ctx context.Context, cmd, dir string, last relay.Turn, stderr io.Writer) (relay.Turn, bool) {
	for {
		t, ok, err := relay.ReadTurn(dir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s : marqueur de tour : %v\n", cmd, err)
		}
		if ok && err == nil && !sameTurn(t, last) {
			return t, true
		}
		if !pause(ctx, relayPoll) {
			return relay.Turn{}, false
		}
	}
}

// relayAgent attend les tours dans le volume de la tâche, en lecture seule
// pour lui. Il ignore un marqueur d'une passe périmée, fait tourner l'agent
// au plus jusqu'à l'échéance du tour, annonce sa fin dans sa sortie, puis ne
// laisse rien au tour suivant : aucun autre processus, ni HOME, ni
// CLAUDE_CONFIG_DIR, ni dossier temporaire.
func relayAgent(ctx context.Context, d stage.Dirs, once func(ctx context.Context) int, stdout, stderr io.Writer) int {
	if requirePID1 && os.Getpid() != 1 {
		_, _ = fmt.Fprintln(stderr, "agent : en relais, l'agent doit être le PID 1 de son conteneur (init: false) : après chaque tour, il tue tous les autres processus qu'il peut atteindre")
		return 2
	}
	var last relay.Turn
	for n := 0; more(ctx, n); {
		turn, ok := nextTurn(ctx, "agent", d.State, last, stderr)
		if !ok {
			return 0
		}
		last = turn
		if turn.Expired(time.Now()) {
			_, _ = fmt.Fprintf(stdout, "marqueur du tour %d de la passe %s périmé, ignoré\n", turn.Round, turn.Pass)
			continue
		}
		// Après un redémarrage, un tour déjà annoncé fini ne se rejoue pas.
		if done, ok, _ := relay.ReadDone(d.OutDir()); ok && done.Matches(turn) {
			continue
		}
		n++
		if turn.Ticket {
			rctx, cancel := context.WithDeadline(ctx, turn.Deadline)
			_ = once(rctx)
			cancel()
			if err := relay.WriteDone(d.OutDir(), relay.Done{Pass: turn.Pass, Round: turn.Round}); err != nil {
				_, _ = fmt.Fprintf(stderr, "agent : annonce de la fin du tour : %v\n", err)
			}
		}
		if err := cleanAfterRound(); err != nil {
			_, _ = fmt.Fprintf(stderr, "agent : nettoyage après le tour : %v\n", err)
		}
	}
	return 0
}

// cleanAfterRound tue tous les autres processus du conteneur, puis vide HOME,
// CLAUDE_CONFIG_DIR et le dossier temporaire : rien de ce que l'agent y a
// laissé ne sert au tour suivant.
func cleanAfterRound() error {
	err := killOthers()
	return errors.Join(err, clearDirs([]string{os.Getenv("HOME"), os.Getenv("CLAUDE_CONFIG_DIR"), os.TempDir()}))
}

// killAllButSelf signale SIGKILL à tous les processus que l'appelant peut
// atteindre, sauf lui-même (kill -1), puis recueille les processus
// orphelins, qu'un PID 1 doit attendre.
func killAllButSelf() error {
	if err := syscall.Kill(-1, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
		switch {
		case errors.Is(err, syscall.ECHILD):
			return nil
		case err != nil && !errors.Is(err, syscall.EINTR):
			return err
		case pid == 0:
			time.Sleep(50 * time.Millisecond)
		}
	}
	return errors.New("des processus survivent au signal de fin")
}

// clearDirs vide chaque dossier de dirs, sans le supprimer : ce sont des
// points de montage. Un dossier vide de nom est ignoré ; la racine est
// refusée.
func clearDirs(dirs []string) error {
	var errs []error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if filepath.Clean(dir) == string(filepath.Separator) {
			errs = append(errs, errors.New("refus de vider la racine"))
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil { // #nosec G703 -- dossier fixé par le déploiement, entrée lue dans ce dossier

				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// relayPublish attend les tours dans le volume de sa tâche. Pour chacun, il
// attend la fin annoncée par l'agent ou l'échéance, copie la sortie de
// l'agent, puis ne publie que la copie. À l'échéance, le tour compte comme
// un agent sans résultat.
func relayPublish(ctx context.Context, rep *reporter, d stage.Dirs,
	once func(ctx context.Context, d stage.Dirs, pre *harness.Result) (int, *pass.Report, []string)) int {
	var last relay.Turn
	for n := 0; more(ctx, n); {
		turn, ok := nextTurn(ctx, rep.stage, d.State, last, rep.stderr)
		if !ok {
			return 0
		}
		last = turn
		if turn.Expired(time.Now()) {
			_, _ = fmt.Fprintf(rep.stderr, "%s : marqueur du tour %d de la passe %s périmé, ignoré\n", rep.stage, turn.Round, turn.Pass)
			continue
		}
		n++
		rep.send(ctx, turn.Pass, relay.Event{Type: relay.Started})
		work, pre, cleanup := d, (*harness.Result)(nil), func() {}
		if turn.Ticket {
			if !waitDone(ctx, d.OutDir(), turn) {
				if ctx.Err() != nil {
					return 0
				}
				pre = &harness.Result{Outcome: harness.Failed, Reason: fmt.Sprintf("%s %d dépassée : l'agent n'a pas annoncé sa fin à temps", deadlineReason, turn.Round)}
			} else {
				work, pre, cleanup = copyOut(d)
				afterCopy(d)
			}
		}
		code, report, incidents := once(ctx, work, pre)
		cleanup()
		rep.incidents(ctx, turn.Pass, incidents)
		rep.finished(ctx, turn.Pass, code)
		// pass-finished clôt la passe : il part en dernier.
		if report != nil {
			rep.send(ctx, turn.Pass, relay.Event{Type: relay.PassFinished, Report: report})
		}
	}
	return 0
}

// waitDone attend que l'agent annonce la fin du tour, jusqu'à son échéance.
func waitDone(ctx context.Context, out string, turn relay.Turn) bool {
	for {
		if done, ok, err := relay.ReadDone(out); ok && err == nil && done.Matches(turn) {
			return true
		}
		left := time.Until(turn.Deadline)
		if left <= 0 || !pause(ctx, min(relayPoll, left)) {
			return false
		}
	}
}

// copyOut copie la sortie de l'agent hors de sa portée ; une sortie refusée
// rend le tour en échec.
func copyOut(d stage.Dirs) (stage.Dirs, *harness.Result, func()) {
	tmp, err := os.MkdirTemp("", "night-shift-out-")
	if err != nil {
		return d, &harness.Result{Outcome: harness.Failed, Reason: "copie de la sortie de l'agent : " + err.Error()}, func() {}
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	c, err := d.CopyOut(tmp)
	if err != nil {
		return d, &harness.Result{Outcome: harness.Failed, Reason: "résultat de l'agent refusé : " + err.Error()}, cleanup
	}
	return c, nil, cleanup
}
