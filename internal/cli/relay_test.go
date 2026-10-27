package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/relay/relaytest"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

// relaySelect fait réclamer une passe à select en relais, puis la
// sélectionner.
func (w *world) relaySelect(t *testing.T, srv *relaytest.Server, roundTimeout string) {
	t.Helper()
	t.Setenv(cli.TokenEnv, secret)
	code, stdout, stderr := run("select", "--relay", "--control", srv.URL, "--round-timeout", roundTimeout,
		"--repo", "o/a", "--api-url", w.api, "--git-url", w.remote.Base,
		"--state", w.state, "--work", w.work, "--next-state", w.state2, "--next-work", w.work2)
	if code != 0 {
		t.Fatalf("select : code %d\n%s\n%s", code, stdout, stderr)
	}
}

// relayAgent fait tourner l'agent en relais sur les volumes donnés, avec
// HOME, CLAUDE_CONFIG_DIR et TMPDIR propres au test ; il les rend.
func (w *world) relayAgent(t *testing.T, mode, state, work string) (home, config, tmp string) {
	t.Helper()
	for _, k := range cli.ForgeSecretEnv() {
		t.Setenv(k, "")
	}
	home, config, tmp = t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")
	t.Setenv("FAKE_CLAUDE", mode)
	t.Setenv("FAKE_CLAUDE_LOG", w.log)
	bin, err := filepath.Abs("../harness/claude/testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("agent", "--relay", "--claude", bin, "--state", state, "--work", work)
	if code != 0 {
		t.Fatalf("agent : code %d\n%s\n%s", code, stdout, stderr)
	}
	return home, config, tmp
}

// steps rend les événements d'une passe sous la forme étape:type.
func steps(srv *relaytest.Server, passID string) []string {
	var out []string
	for _, e := range srv.Events(passID) {
		out = append(out, e.Stage+":"+string(e.Type))
	}
	return out
}

// killBackground tue le processus que le faux agent hostile a laissé.
func killBackground(t *testing.T, w *world) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.log, "background.pid"))
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestRelayCarriesAPassFromClaimToPassFinished(t *testing.T) {
	w := newWorld(t)
	srv := relaytest.New(t)
	t.Cleanup(func() { killBackground(t, w) })
	killedAfterDone := false
	cli.Relay(t, cli.RelayKnobs{Kill: func() error {
		// Le vrai kill -1 ne se joue qu'en PID 1 d'un conteneur (#111) : ici,
		// on constate qu'il vient après l'annonce de fin, et on tue à sa
		// place le processus laissé par l'agent.
		_, err := os.Stat(filepath.Join(w.work, "out", relay.DoneFile))
		killedAfterDone = err == nil
		killBackground(t, w)
		return nil
	}})
	srv.Request("pass-r1", relay.Manual)

	w.relaySelect(t, srv, "5s")
	turn, ok, err := relay.ReadTurn(w.state)
	if err != nil || !ok || turn.Pass != "pass-r1" || turn.Round != 1 || !turn.Ticket {
		t.Fatalf("marqueur du premier tour = %+v, %v, %v", turn, ok, err)
	}
	task, ok, err := (stage.Dirs{State: w.state, Work: w.work}).ReadTask()
	if err != nil || !ok || task.PassID != "pass-r1" {
		t.Fatalf("tâche = %+v, %v, %v ; attendu la passe réclamée", task, ok, err)
	}

	home, config, tmp := w.relayAgent(t, "hostile", w.state, w.work)

	if done, ok, err := relay.ReadDone(filepath.Join(w.work, "out")); err != nil || !ok || !done.Matches(turn) {
		t.Fatalf("fin annoncée = %+v, %v, %v", done, ok, err)
	}
	if !killedAfterDone {
		t.Fatal("les autres processus doivent être tués après l'annonce de fin")
	}
	for _, dir := range []string{home, config, tmp} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("%s n'est pas vide après le tour : %v", dir, entries)
		}
	}

	w.publishStage(t, "--relay", "--control", srv.URL)

	if len(w.gh.pulls) != 1 || len(w.gh.digests) != 1 {
		t.Fatalf("PR = %d, digests = %d", len(w.gh.pulls), len(w.gh.digests))
	}
	want := []string{"select:started", "select:finished", "publish:started", "publish:finished", "publish:pass-finished"}
	if got := steps(srv, "pass-r1"); !slices.Equal(got, want) {
		t.Fatalf("événements = %v, attendu %v", got, want)
	}
	events := srv.Events("pass-r1")
	if r := events[4].Report; r == nil || r.ID != "pass-r1" || len(r.Tickets) != 1 || r.Tickets[0].ChangeRequest == "" {
		t.Fatalf("rapport de fin = %+v, attendu le ticket et sa PR", r)
	}
	if *events[1].Exit != 0 || *events[3].Exit != 0 {
		t.Fatalf("codes de sortie = %d, %d", *events[1].Exit, *events[3].Exit)
	}
}

func TestRelayAgentIgnoresATurnOfAStalePass(t *testing.T) {
	w := newWorld(t)
	cli.Relay(t, cli.RelayKnobs{Timeout: 200 * time.Millisecond})
	w.selectStage(t)
	if err := relay.WriteTurn(w.state, relay.Turn{Pass: "pass-old", Round: 1, Ticket: true, Deadline: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}

	w.relayAgent(t, "commit", w.state, w.work)

	if _, err := os.Stat(filepath.Join(w.log, "args")); err == nil {
		t.Fatal("l'agent a tourné sur le marqueur d'une passe périmée")
	}
	if _, err := os.Stat(filepath.Join(w.work, "out", relay.DoneFile)); err == nil {
		t.Fatal("fin annoncée pour un tour ignoré")
	}
}

func TestRelayPublishTreatsAMissedDeadlineAsAFailedAgent(t *testing.T) {
	w := newWorld(t)
	srv := relaytest.New(t)
	cli.Relay(t, cli.RelayKnobs{})
	srv.Request("pass-r2", relay.Schedule)

	w.relaySelect(t, srv, "100ms")
	// Aucun agent n'annonce sa fin.
	w.publishStage(t, "--relay", "--control", srv.URL)

	if len(w.gh.pulls) != 0 {
		t.Fatalf("PR ouvertes : %v", w.gh.pulls)
	}
	if got := w.lifecycle(); !slices.Equal(got, []string{"ready-for-human"}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [ready-for-human]", got)
	}
	if last := w.gh.comments[len(w.gh.comments)-1]; !strings.Contains(last, "échéance du tour 1 dépassée") {
		t.Fatalf("commentaire final = %q", last)
	}
	if got := steps(srv, "pass-r2"); !slices.Contains(got, "publish:pass-finished") {
		t.Fatalf("événements = %v, attendu la passe close", got)
	}
}

func TestRelayPublishPushesWhatItCopiedEvenIfTheOutputChangesAfterward(t *testing.T) {
	w := newWorld(t)
	srv := relaytest.New(t)
	rewritten := false
	cli.Relay(t, cli.RelayKnobs{AfterCopy: func(d stage.Dirs) {
		// L'agent a annoncé sa fin, puis réécrit sa sortie : un échec, sans
		// série.
		_ = os.RemoveAll(filepath.Join(d.OutDir(), "patches"))
		rewritten = os.WriteFile(filepath.Join(d.OutDir(), "result.json"), []byte(`{"outcome":"failed","reason":"réécrit"}`), 0o600) == nil
	}})
	srv.Request("pass-r3", relay.Manual)

	w.relaySelect(t, srv, "5s")
	w.relayAgent(t, "commit", w.state, w.work)
	w.publishStage(t, "--relay", "--control", srv.URL)

	if !rewritten {
		t.Fatal("la sortie de l'agent n'a pas été réécrite après la copie")
	}
	if len(w.gh.pulls) != 1 {
		t.Fatalf("PR ouvertes : %d, attendu la série copiée avant la réécriture", len(w.gh.pulls))
	}
	if log := w.remote.Out("log", "--format=%s", "main..agent/7-ajouter-f"); log != "test: y\nfeat: x" {
		t.Fatalf("historique publié :\n%s", log)
	}
}

func TestRelayRetryRoundIsHandedOverByItsOwnTurn(t *testing.T) {
	w := newWorld(t)
	w.gh.red = 1
	srv := relaytest.New(t)
	cli.Relay(t, cli.RelayKnobs{})
	srv.Request("pass-r4", relay.Manual)

	w.relaySelect(t, srv, "5s")
	w.relayAgent(t, "commit", w.state, w.work)
	w.publishStage(t, "--relay", "--control", srv.URL, "--round-timeout", "5s", "--next-state", w.state2, "--next-work", w.work2)

	turn, ok, err := relay.ReadTurn(w.state2)
	if err != nil || !ok || turn.Pass != "pass-r4" || turn.Round != 2 || !turn.Ticket {
		t.Fatalf("marqueur de la relance = %+v, %v, %v", turn, ok, err)
	}
	w.relayAgent(t, "fix", w.state2, w.work2)
	w.publishStage(t, "--relay", "--control", srv.URL, "--follow-up", "--state", w.state2, "--work", w.work2)

	want := []string{"select:started", "select:finished", "publish:started", "publish:finished",
		"retry-publish:started", "retry-publish:finished", "retry-publish:pass-finished"}
	if got := steps(srv, "pass-r4"); !slices.Equal(got, want) {
		t.Fatalf("événements = %v, attendu %v", got, want)
	}
	if last := w.gh.comments[len(w.gh.comments)-1]; !strings.Contains(last, "CI verte au tour 2") {
		t.Fatalf("commentaire final = %q", last)
	}
}

func TestRelayOptionsAreCheckedBeforeAnything(t *testing.T) {
	w := newWorld(t)
	t.Setenv(cli.TokenEnv, secret)
	for name, args := range map[string][]string{
		"select sans --control": {"select", "--relay", "--repo", "o/a", "--state", w.state, "--work", w.work},
		"publish sans --relay":  {"publish", "--control", "http://control", "--state", w.state, "--work", w.work},
		"échéance nulle":        {"publish", "--relay", "--control", "http://control", "--round-timeout", "0s", "--state", w.state, "--work", w.work},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, stderr := run(args...); code != 2 {
				t.Fatalf("code = %d, attendu 2\n%s", code, stderr)
			}
		})
	}
}

func TestRelayAgentRefusesToRunOutsidePID1(t *testing.T) {
	w := newWorld(t)
	cli.Relay(t, cli.RelayKnobs{RequirePID1: true})
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")

	code, _, stderr := run("agent", "--relay", "--state", w.state, "--work", w.work)

	if code != 2 || !strings.Contains(stderr, "PID 1") {
		t.Fatalf("code = %d, stderr = %q ; attendu un refus qui nomme le PID 1", code, stderr)
	}
}
