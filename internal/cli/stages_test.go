package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

// fakeGitHub est une API REST GitHub minimale, à un dépôt o/a et un ticket
// #7, qui garde l'état des labels, des commentaires et des PR.
type fakeGitHub struct {
	mu       sync.Mutex
	labels   []string
	comments []string
	pulls    []map[string]any
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/a/contents/.night-shift/opt-in.yaml":
		_, _ = io.WriteString(w, "version: 1\ntrustLevel: {ticketsPerPass: 1, pullRequests: draft}\n")
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/a/issues":
		if !slices.Contains(g.labels, "ready-for-agent") {
			_, _ = io.WriteString(w, "[]")
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number": 7, "title": "Ajouter f", "body": "Le brief.", "created_at": "2026-10-01T10:00:00Z",
			"user": map[string]any{"login": "alice"}, "labels": []map[string]any{{"name": "ready-for-agent"}},
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/a/issues/7/labels":
		var body struct{ Labels []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, l := range body.Labels {
			if !slices.Contains(g.labels, l) {
				g.labels = append(g.labels, l)
			}
		}
		_, _ = io.WriteString(w, "[]")
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/o/a/issues/7/labels/"):
		l := strings.TrimPrefix(r.URL.Path, "/repos/o/a/issues/7/labels/")
		g.labels = slices.DeleteFunc(g.labels, func(s string) bool { return s == l })
		_, _ = io.WriteString(w, "[]")
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/a/issues/7/comments":
		var body struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.comments = append(g.comments, body.Body)
		_, _ = io.WriteString(w, "{}")
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/a/pulls":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.pulls = append(g.pulls, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"html_url": "https://github.example/o/a/pull/%d"}`, len(g.pulls))
	default:
		http.NotFound(w, r)
	}
}

type world struct {
	gh               *fakeGitHub
	api              string
	remote           *testgit.Remote
	state, work, log string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	src := testgit.New(t)
	src.Write("README.md", "bonjour\n")
	src.Commit("init")
	w := &world{
		gh:     &fakeGitHub{labels: []string{"ready-for-agent", "prio:P2"}},
		remote: testgit.NewRemote(t, "o/a", src),
		state:  t.TempDir(), work: t.TempDir(), log: t.TempDir(),
	}
	srv := httptest.NewServer(w.gh)
	t.Cleanup(srv.Close)
	w.api = srv.URL
	return w
}

func (w *world) selectStage(t *testing.T) {
	t.Helper()
	t.Setenv(cli.TokenEnv, secret)
	code, stdout, stderr := run("select", "--repo", "o/a", "--api-url", w.api, "--git-url", w.remote.Base, "--state", w.state, "--work", w.work)
	if code != 0 {
		t.Fatalf("select : code %d\n%s\n%s", code, stdout, stderr)
	}
	if reserved := strings.Contains(stdout, "réservé"); reserved == strings.Contains(stdout, "aucun ticket prêt éligible") {
		t.Fatalf("rapport de select contradictoire :\n%s", stdout)
	}
}

func (w *world) agentStage(t *testing.T, mode string) {
	t.Helper()
	for _, k := range []string{cli.TokenEnv, "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")
	t.Setenv("FAKE_CLAUDE", mode)
	t.Setenv("FAKE_CLAUDE_LOG", w.log)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	bin, err := filepath.Abs("../harness/claude/testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("agent", "--claude", bin, "--state", w.state, "--work", w.work)
	if code != 0 {
		t.Fatalf("agent : code %d\n%s\n%s", code, stdout, stderr)
	}
}

func (w *world) publishStage(t *testing.T) string {
	t.Helper()
	t.Setenv(cli.TokenEnv, secret)
	code, stdout, stderr := run("publish", "--api-url", w.api, "--git-url", w.remote.Base, "--state", w.state, "--work", w.work)
	if code != 0 {
		t.Fatalf("publish : code %d\n%s\n%s", code, stdout, stderr)
	}
	return stdout
}

func (w *world) lifecycle() []string {
	var out []string
	for _, l := range w.gh.labels {
		if l != "prio:P2" {
			out = append(out, l)
		}
	}
	return out
}

func TestThreeStagesTurnAReadyTicketIntoADraftPR(t *testing.T) {
	w := newWorld(t)

	w.selectStage(t)
	w.agentStage(t, "commit")
	stdout := w.publishStage(t)

	if len(w.gh.pulls) != 1 {
		t.Fatalf("PR ouvertes : %v", w.gh.pulls)
	}
	pr := w.gh.pulls[0]
	if pr["draft"] != true || pr["head"] != "agent/7-ajouter-f" || pr["base"] != "main" || !strings.Contains(fmt.Sprint(pr["body"]), "Closes #7") {
		t.Fatalf("PR = %v", pr)
	}
	log := w.remote.Out("log", "--format=%an|%s|%(trailers:key=Night-Shift-Pass,valueonly)", "main..agent/7-ajouter-f")
	if !strings.Contains(log, "night-shift agent|test: y|pass-") || !strings.Contains(log, "night-shift agent|feat: x|pass-") {
		t.Fatalf("historique publié :\n%s", log)
	}
	if got := w.lifecycle(); len(got) != 0 {
		t.Fatalf("labels de cycle de vie restants : %v", got)
	}
	if last := w.gh.comments[len(w.gh.comments)-1]; !strings.Contains(last, "https://github.example/o/a/pull/1") {
		t.Fatalf("commentaire final = %q", last)
	}
	if !strings.Contains(stdout, "succeeded") || !strings.Contains(stdout, "https://github.example/o/a/pull/1") {
		t.Fatalf("rapport = %q", stdout)
	}
}

func TestNeedsInfoAgentLeavesTicketInNeedsInfo(t *testing.T) {
	w := newWorld(t)

	w.selectStage(t)
	w.agentStage(t, "needs-info")
	w.publishStage(t)

	if got := w.lifecycle(); !slices.Equal(got, []string{"needs-info"}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [needs-info]", got)
	}
	if len(w.gh.pulls) != 0 {
		t.Fatal("aucune PR attendue")
	}
}

func TestInterruptedAgentHandsTicketToHuman(t *testing.T) {
	w := newWorld(t)

	w.selectStage(t)
	// Le conteneur de l'agent a été tué : il n'a rendu aucun résultat.
	w.publishStage(t)

	if got := w.lifecycle(); !slices.Equal(got, []string{"ready-for-human"}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [ready-for-human]", got)
	}
	if last := w.gh.comments[len(w.gh.comments)-1]; !strings.Contains(last, "interrompu") {
		t.Fatalf("commentaire final = %q, attendu la mention de l'interruption", last)
	}
}

func TestFailedCloneHandsTheReservedTicketToAHuman(t *testing.T) {
	w := newWorld(t)
	t.Setenv(cli.TokenEnv, secret)

	code, _, _ := run("select", "--repo", "o/a", "--api-url", w.api, "--git-url", t.TempDir(), "--state", w.state, "--work", w.work)

	if code != 1 {
		t.Fatalf("code = %d, attendu 1", code)
	}
	if got := w.lifecycle(); !slices.Equal(got, []string{"ready-for-human"}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [ready-for-human]", got)
	}
	if _, err := os.Stat(filepath.Join(w.state, "task.json")); err == nil {
		t.Fatal("aucune tâche ne doit être confiée à l'agent")
	}
}

func TestEmptyQueueRunsAllStagesAsNoOps(t *testing.T) {
	w := newWorld(t)
	w.gh.labels = nil

	w.selectStage(t)
	w.agentStage(t, "commit")
	stdout := w.publishStage(t)

	if !strings.Contains(stdout, "rien à publier") || len(w.gh.comments) != 0 {
		t.Fatalf("rapport = %q, commentaires = %v", stdout, w.gh.comments)
	}
}

func TestSelectLeavesNoForgeTokenInTheWorkVolume(t *testing.T) {
	w := newWorld(t)

	w.selectStage(t)

	for _, root := range []string{w.work, w.state} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), secret) {
				t.Errorf("jeton de forge trouvé dans %s", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentWithAForgeTokenNeverRunsAndTheTicketGoesToAHuman(t *testing.T) {
	w := newWorld(t)
	w.selectStage(t)
	t.Setenv(cli.TokenEnv, "")
	t.Setenv("GH_TOKEN", secret)
	t.Setenv("FAKE_CLAUDE_LOG", w.log)

	code, _, stderr := run("agent", "--claude", "/bin/false", "--state", w.state, "--work", w.work)

	if code != 0 || !strings.Contains(stderr, "GH_TOKEN") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	t.Setenv("GH_TOKEN", "")
	w.publishStage(t)
	if got := w.lifecycle(); !slices.Equal(got, []string{"ready-for-human"}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [ready-for-human]", got)
	}
	if last := w.gh.comments[len(w.gh.comments)-1]; !strings.Contains(last, "agent non démarré") {
		t.Fatalf("commentaire final = %q", last)
	}
}

func TestAgentRefusesToStartWithAForgeToken(t *testing.T) {
	for _, k := range []string{cli.TokenEnv, "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, secret)
			code, _, stderr := run("agent", "--state", t.TempDir(), "--work", t.TempDir())
			if code != 2 || !strings.Contains(stderr, k) {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
		})
	}
}
