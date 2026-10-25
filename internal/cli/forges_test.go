package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
)

func TestUnknownForgeIsAUsageErrorWithoutAnyNetworkCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("appel réseau inattendu : %s %s", r.Method, r.URL)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(cli.TokenEnv, secret)
	for _, args := range [][]string{
		{"select", "--repo", "o/a", "--state", t.TempDir(), "--work", t.TempDir()},
		{"publish", "--state", t.TempDir(), "--work", t.TempDir()},
		{"pass", "--repo", "o/a"},
	} {
		t.Run(args[0], func(t *testing.T) {
			code, _, stderr := run(append(args, "--forge", "nope", "--api-url", srv.URL)...)
			if code != 2 || !strings.Contains(stderr, `"nope"`) || !strings.Contains(stderr, "github") {
				t.Fatalf("code = %d, stderr = %q ; attendu 2, la forge inconnue et la liste des forges", code, stderr)
			}
		})
	}
}

func TestAgentRecognisesAForgeTokenByNameOrByShape(t *testing.T) {
	for env, want := range map[string]string{
		"GH_TOKEN=abc":                           "GH_TOKEN",
		"MY_SECRET=ghp_0123456789":               "MY_SECRET",
		"CI_JOB=github_pat_11AAAA":               "CI_JOB",
		"X=gho_x":                                "X",
		"X=ghs_x":                                "X",
		"X=ghu_x":                                "X",
		"GITLAB=glpat-xyz":                       "GITLAB",
		"PADDED= ghp_0123":                       "PADDED",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x": "",
		"GH_TOKEN=":                              "",
	} {
		t.Run(env, func(t *testing.T) {
			for _, k := range cli.ForgeSecretEnv() {
				t.Setenv(k, "")
			}
			k, v, _ := strings.Cut(env, "=")
			t.Setenv(k, v)
			code, _, stderr := run("agent", "--state", t.TempDir(), "--work", t.TempDir())
			if want == "" {
				if code != 0 {
					t.Fatalf("code = %d, stderr = %q ; attendu aucun jeton reconnu", code, stderr)
				}
				return
			}
			if code != 2 || !strings.Contains(stderr, want) {
				t.Fatalf("code = %d, stderr = %q ; attendu le refus de %s", code, stderr, want)
			}
		})
	}
}

// fakeForge inscrit au registre une forge en mémoire, nommée fake, dont le
// jeton est dans FAKE_FORGE_TOKEN et qui sert le dépôt o/a du monde w. Ses
// adresses par défaut sont fake://api et les dépôts nus de w.
func fakeForge(t *testing.T, w *world) *memforge.Forge {
	t.Helper()
	f := memforge.New()
	f.GitURL = w.remote.Base
	f.SetFile("o/a", ".night-shift/opt-in.yaml", testOptIn)
	f.AddIssue(forge.Ticket{Repo: "o/a", Number: 7, Title: "Ajouter f", Body: "Le brief.", Author: "alice", AuthorAssociated: true, Labels: []string{forge.LabelReady}})
	cli.AddForge(t, "fake", "FAKE_FORGE_TOKEN", "fake://api", w.remote.Base, func(apiURL, gitURL, token string) cli.Forge {
		if token != secret || apiURL != "fake://api" || gitURL != w.remote.Base {
			t.Errorf("adaptateur construit avec %q, %q, %q ; attendu ses adresses par défaut et le jeton", apiURL, gitURL, token)
		}
		return f
	})
	return f
}

func TestMissingTokenNamesTheVariableOfTheChosenForge(t *testing.T) {
	w := newWorld(t)
	fakeForge(t, w)
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv("FAKE_FORGE_TOKEN", "")

	code, _, stderr := run("select", "--forge", "fake", "--repo", "o/a", "--state", w.state, "--work", w.work)

	if code != 2 || !strings.Contains(stderr, "FAKE_FORGE_TOKEN") || strings.Contains(stderr, cli.TokenEnv) {
		t.Fatalf("code = %d, stderr = %q ; attendu la variable de la forge fake", code, stderr)
	}
}

func TestAForgeAddedToTheRegistryServesSelectAndPublish(t *testing.T) {
	w := newWorld(t)
	f := fakeForge(t, w)
	t.Setenv(cli.TokenEnv, "")
	t.Setenv("FAKE_FORGE_TOKEN", secret)

	code, stdout, stderr := run("select", "--forge", "fake", "--repo", "o/a", "--state", w.state, "--work", w.work)
	if code != 0 || !strings.Contains(stdout, "o/a#7 réservé") {
		t.Fatalf("select : code %d\n%s\n%s", code, stdout, stderr)
	}
	// L'étape agent refuse le jeton de cette forge comme celui de GitHub.
	code, _, stderr = run("agent", "--state", w.state, "--work", w.work, "--claude", "/bin/false")
	if code != 0 || !strings.Contains(stderr, "FAKE_FORGE_TOKEN") {
		t.Fatalf("agent : code = %d, stderr = %q ; attendu le refus du jeton de la forge fake", code, stderr)
	}
	t.Setenv("FAKE_FORGE_TOKEN", "")
	w.agentStage(t, "commit")
	t.Setenv("FAKE_FORGE_TOKEN", secret)
	code, stdout, stderr = run("publish", "--forge", "fake", "--ci-timeout", "200ms", "--ci-poll", "1ms", "--ci-settle", "2ms", "--state", w.state, "--work", w.work)

	if code != 0 || len(f.ChangeRequests("o/a")) != 1 || len(w.gh.pulls) != 0 {
		t.Fatalf("publish : code %d, demandes de changement %v\n%s\n%s", code, f.ChangeRequests("o/a"), stdout, stderr)
	}
	if labels := f.Issue("o/a", 7).Labels; len(labels) != 0 {
		t.Fatalf("labels du ticket = %v, attendu le cycle de vie terminé", labels)
	}
	if len(f.CreatedIssues("o/a")) != 1 || len(w.gh.digests) != 0 {
		t.Fatalf("digests = %v, attendu le digest sur la forge fake seulement", f.CreatedIssues("o/a"))
	}
}

func TestPublishRefusesATaskReservedOnAnotherForge(t *testing.T) {
	w := newWorld(t)
	fakeForge(t, w)
	w.selectStage(t)
	w.agentStage(t, "commit")
	t.Setenv("FAKE_FORGE_TOKEN", secret)

	code, _, stderr := run("publish", "--forge", "fake", "--state", w.state, "--work", w.work)

	if code != 2 || !strings.Contains(stderr, "github") || !strings.Contains(stderr, "fake") {
		t.Fatalf("code = %d, stderr = %q ; attendu le refus de la tâche réservée sur github", code, stderr)
	}
	if len(w.gh.pulls) != 0 {
		t.Fatal("aucune demande de changement ne doit être ouverte")
	}
}

func TestATaskWithoutForgeComesFromGitHub(t *testing.T) {
	w := newWorld(t)
	w.selectStage(t)
	task := readJSON(t, filepath.Join(w.state, "task.json"))
	if task["Forge"] != "github" {
		t.Fatalf("forge de la tâche = %v, attendu github", task["Forge"])
	}
	delete(task, "Forge")
	writeJSON(t, filepath.Join(w.state, "task.json"), task)
	w.agentStage(t, "commit")

	stdout := w.publishStage(t)

	if len(w.gh.pulls) != 1 {
		t.Fatalf("PR ouvertes : %v\n%s", w.gh.pulls, stdout)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
