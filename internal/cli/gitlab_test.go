package cli_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab/gitlabtest"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

const glSecret = "glpat-cli-secret-token"

// glWorld est un projet g/sub/p, rangé dans un sous-groupe, sur une API
// GitLab simulée et un dépôt git local : son ticket #7 est prêt, écrit par
// une Developer, et toute pipeline passe.
type glWorld struct {
	*world
	gl     *gitlabtest.Server
	remote *testgit.Remote
	// outputs sont les sorties de toutes les étapes lancées.
	outputs []string
}

const glRepo = "g/sub/p"

func newGitLabWorld(t *testing.T) *glWorld {
	t.Helper()
	t.Setenv(cli.WebhookEnv, "")
	src := testgit.New(t)
	src.Write("README.md", "bonjour\n")
	src.Write(".night-shift/opt-in.yaml", testOptIn)
	src.Commit("init")
	gl := gitlabtest.New(t, glSecret)
	gl.SetFile(glRepo, ".night-shift/opt-in.yaml", testOptIn)
	gl.AddMember(glRepo, "alice", gitlab.Developer, true)
	gl.AddIssue(glRepo, gitlabtest.Issue{IID: 7, Title: "Ajouter f", Description: "Le brief.", Author: "alice", Labels: []string{forge.LabelReady, "prio:P2"}})
	gl.SetPipeline("", "", "success", gitlabtest.Job{Name: "test", Status: "success", WebURL: "https://gitlab.example/ci/1"})
	return &glWorld{
		world:  &world{state: t.TempDir(), work: t.TempDir(), log: t.TempDir()},
		gl:     gl,
		remote: testgit.NewRemote(t, glRepo, src),
	}
}

// stage lance une étape de confiance sur GitLab, avec le seul jeton GitLab.
func (w *glWorld) stage(t *testing.T, name string, extra ...string) string {
	t.Helper()
	t.Setenv(cli.TokenEnv, "")
	t.Setenv(cli.GitLabTokenEnv, glSecret)
	args := append([]string{name, "--forge", "gitlab", "--api-url", w.gl.URL, "--git-url", w.remote.Base, "--state", w.state, "--work", w.work}, extra...)
	code, stdout, stderr := run(args...)
	w.outputs = append(w.outputs, stdout, stderr)
	if code != 0 {
		t.Fatalf("%s : code %d\n%s\n%s", name, code, stdout, stderr)
	}
	return stdout
}

func (w *glWorld) pass(t *testing.T, agentMode string) string {
	t.Helper()
	if out := w.stage(t, "select", "--repo", glRepo); !strings.Contains(out, glRepo+"#7 réservé") {
		t.Fatalf("select :\n%s", out)
	}
	w.agentStage(t, agentMode)
	return w.stage(t, "publish", "--ci-timeout", "200ms", "--ci-poll", "1ms", "--ci-settle", "2ms")
}

func (w *glWorld) lifecycle() []string {
	return slices.DeleteFunc(w.gl.Issue(glRepo, 7).Labels, func(l string) bool { return l == "prio:P2" })
}

func (w *glWorld) lastNote(t *testing.T) string {
	t.Helper()
	notes := w.gl.Issue(glRepo, 7).Notes
	if len(notes) == 0 {
		t.Fatal("aucune note sur le ticket")
	}
	return notes[len(notes)-1].Body
}

func TestGitLabStagesTurnAReadyTicketIntoADraftMergeRequest(t *testing.T) {
	w := newGitLabWorld(t)

	stdout := w.pass(t, "commit")

	mrs := w.gl.MergeRequests(glRepo)
	if len(mrs) != 1 {
		t.Fatalf("merge requests = %+v\n%s", mrs, stdout)
	}
	mr := mrs[0]
	if !mr.Draft() || mr.Title != "Draft: Ajouter f" || mr.SourceBranch != "agent/7-ajouter-f" || mr.TargetBranch != "main" ||
		!strings.HasPrefix(mr.Description, "Closes #7") || mr.RemoveSourceBranch {
		t.Fatalf("merge request = %+v", mr)
	}
	log := w.remote.Out("log", "--format=%an <%ae>|%s", "main..agent/7-ajouter-f")
	if !strings.Contains(log, "night-shift agent <night-shift-agent@users.noreply.localhost>|feat: x") {
		t.Fatalf("historique publié :\n%s", log)
	}
	if got := w.lifecycle(); len(got) != 0 {
		t.Fatalf("labels de cycle de vie restants : %v", got)
	}
	mrURL := "https://gitlab.example/g/sub/p/-/merge_requests/1"
	if last := w.lastNote(t); !strings.Contains(last, mrURL) {
		t.Fatalf("note finale = %q", last)
	}
	issues := w.gl.Issues(glRepo)
	if len(issues) != 2 || !strings.Contains(issues[1].Description, mrURL) {
		t.Fatalf("issues = %+v, attendu un digest qui renvoie vers la merge request", issues)
	}
	if !strings.Contains(stdout, "succeeded") || !strings.Contains(stdout, "digest "+glRepo+" : https://gitlab.example/g/sub/p/-/issues/8") {
		t.Fatalf("rapport = %q", stdout)
	}
	for _, out := range w.outputs {
		if strings.Contains(out, glSecret) {
			t.Fatalf("le jeton apparaît dans une sortie :\n%s", out)
		}
	}
}

func TestGitLabZoneRefusalPushesNothingAndHandsTheTicketToAHuman(t *testing.T) {
	w := newGitLabWorld(t)
	mainBefore := w.remote.Out("rev-parse", "main")

	stdout := w.pass(t, "e9")

	if branches := w.remote.Out("branch", "--list", "agent/*"); branches != "" {
		t.Fatalf("branche agent/ poussée : %q", branches)
	}
	if got := w.remote.Out("rev-parse", "main"); got != mainBefore {
		t.Fatal("la branche main a bougé")
	}
	if mrs := w.gl.MergeRequests(glRepo); len(mrs) != 0 {
		t.Fatalf("merge requests = %+v", mrs)
	}
	if got := w.lifecycle(); !slices.Equal(got, []string{forge.LabelHuman}) {
		t.Fatalf("labels de cycle de vie = %v, attendu [%s]", got, forge.LabelHuman)
	}
	if last := w.lastNote(t); !strings.Contains(last, "refus de zone") || !strings.Contains(last, "`.github/workflows/e9.yml`") {
		t.Fatalf("note finale = %q", last)
	}
	if !strings.Contains(stdout, "zone-refusal") || len(w.gl.Issues(glRepo)) != 2 {
		t.Fatalf("publish = %q, attendu le refus et un digest", stdout)
	}
}

func TestAgentRefusesAGitLabToken(t *testing.T) {
	for env, want := range map[string]string{
		cli.GitLabTokenEnv + "=abc": cli.GitLabTokenEnv,
		"GITLAB_TOKEN=abc":          "GITLAB_TOKEN",
		"CI_JOB_TOKEN=abc":          "CI_JOB_TOKEN",
		"DEPLOY=gldt-0123":          "DEPLOY",
		"JOB=glcbt-64_0123":         "JOB",
		"PROJET=" + glSecret:        "PROJET",
	} {
		t.Run(env, func(t *testing.T) {
			for _, k := range cli.ForgeSecretEnv() {
				t.Setenv(k, "")
			}
			k, v, _ := strings.Cut(env, "=")
			t.Setenv(k, v)
			code, _, stderr := run("agent", "--state", t.TempDir(), "--work", t.TempDir())
			if code != 2 || !strings.Contains(stderr, want) || strings.Contains(stderr, v) {
				t.Fatalf("code = %d, stderr = %q ; attendu le refus de %s, sans sa valeur", code, stderr, want)
			}
		})
	}
}

func TestEachForgeChecksTheShapeOfItsRepos(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv(cli.GitLabTokenEnv, glSecret)
	for _, c := range []struct {
		forge, repo string
		valid       bool
	}{
		{"github", "o/a", true},
		{"github", "g/sub/p", false},
		{"github", "o/../../x", false},
		{"gitlab", "g/p", true},
		{"gitlab", "g/sub/sub2/p", true},
		{"gitlab", "p", false},
		{"gitlab", "g/../x", false},
		{"gitlab", "g//p", false},
	} {
		t.Run(c.forge+" "+c.repo, func(t *testing.T) {
			// Un dépôt bien formé atteint l'API, ici injoignable : la passe
			// échoue alors avec le code 1, pas en erreur d'usage.
			code, _, stderr := run("pass", "--forge", c.forge, "--repo", c.repo, "--api-url", "http://127.0.0.1:1")
			if c.valid == (code == 2) {
				t.Fatalf("code = %d, stderr = %q ; attendu le dépôt %s", code, stderr, map[bool]string{true: "accepté", false: "refusé"}[c.valid])
			}
			if !c.valid && !strings.Contains(stderr, c.repo) {
				t.Fatalf("stderr = %q, attendu le dépôt refusé", stderr)
			}
		})
	}
}
