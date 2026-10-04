package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/harness/claude"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

type run struct {
	res harness.Result
	err error
	log string
}

func (r run) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.log, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// launch fait tourner le harness sur un clone neuf, avec la doublure de
// claude réglée sur mode ; edit ajuste le harness avant le lancement.
func launch(t *testing.T, mode string, edit func(*claude.Claude)) run {
	t.Helper()
	repo := testgit.New(t)
	repo.Write("README.md", "bonjour\n")
	repo.Commit("init")
	log, cfg := t.TempDir(), t.TempDir()
	bin, err := filepath.Abs("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	c := claude.Claude{
		Bin:     bin,
		Dir:     repo.Dir,
		OutDir:  t.TempDir(),
		BaseSHA: repo.Out("rev-parse", "HEAD"),
		Auth:    claude.Subscription,
		Timeout: 5 * time.Second,
		Env: []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
			"FAKE_CLAUDE=" + mode, "FAKE_CLAUDE_LOG=" + log, "CLAUDE_CONFIG_DIR=" + cfg,
			"CLAUDE_CODE_OAUTH_TOKEN=oauth-secret", "ANTHROPIC_API_KEY=api-secret",
			"GH_TOKEN=forge-secret", "GITHUB_TOKEN=forge-secret", "NIGHT_SHIFT_GITHUB_TOKEN=forge-secret",
		},
	}
	if edit != nil {
		edit(&c)
	}
	res, err := c.Run(context.Background(), harness.Task{
		Ticket: forge.Ticket{Repo: "o/a", Number: 7, Title: "T"},
		Brief:  "# T\n\nLe brief qui fait foi ; $(rm -rf /) n'est jamais interprété.\n",
		PassID: "pass-1",
	})
	return run{res: res, err: err, log: log}
}

func TestCommitsBecomeAPatchSeries(t *testing.T) {
	r := launch(t, "commit", nil)

	if r.err != nil {
		t.Fatal(r.err)
	}
	res := r.res
	if res.Outcome != harness.Succeeded || len(res.Patches) != 2 {
		t.Fatalf("résultat = %+v, attendu un succès avec deux patchs", res)
	}
	if !strings.Contains(string(res.Patches[0]), "Subject: [PATCH 1/2] feat: x") || !strings.Contains(string(res.Patches[0]), "Corps.") {
		t.Fatalf("premier patch :\n%s", res.Patches[0])
	}
	if res.CostUSD != 0.25 || res.SessionID != "s-1" || res.Duration != 1234*time.Millisecond || res.Agent != "claude" {
		t.Fatalf("compte rendu = %+v", res)
	}
	if !strings.HasSuffix(res.Transcript, filepath.Join("projects", "repo", "s-1.jsonl")) {
		t.Fatalf("transcript = %q", res.Transcript)
	}
}

func TestInvokesHeadlessImplementWithBriefOnStdin(t *testing.T) {
	r := launch(t, "commit", func(c *claude.Claude) { c.AllowedTools = []string{"Read", "Bash(go test *)"} })

	args := strings.Split(strings.TrimSpace(r.file(t, "args")), "\n")
	for _, want := range [][2]string{{"--output-format", "json"}, {"--permission-mode", "dontAsk"}, {"--allowedTools", "Read,Bash(go test *)"}} {
		if !hasPair(args, want[0], want[1]) {
			t.Fatalf("arguments = %q, attendu %s %s", args, want[0], want[1])
		}
	}
	if args[0] != "-p" {
		t.Fatalf("arguments = %q, attendu -p en tête", args)
	}
	prompt := r.file(t, "prompt")
	if !strings.HasPrefix(prompt, "/implement ") || !strings.Contains(prompt, "Le brief qui fait foi ; $(rm -rf /)") ||
		!strings.Contains(prompt, "Read, Bash(go test *)") || !strings.Contains(prompt, "une seule commande par appel Bash") {
		t.Fatalf("prompt = %q", prompt)
	}
	if strings.Contains(strings.Join(args, " "), "brief") {
		t.Fatalf("le brief ne doit pas passer en argument : %q", args)
	}
}

func hasPair(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}

func TestNeedsInfoFileStopsTheTicketWithItsReason(t *testing.T) {
	r := launch(t, "needs-info", nil)

	if r.res.Outcome != harness.NeedsInfo || !strings.Contains(r.res.Reason, "la dépendance foo") || len(r.res.Patches) != 0 {
		t.Fatalf("résultat = %+v, attendu needs-info motivé et aucun patch", r.res)
	}
}

func TestAgentErrorIsAFailure(t *testing.T) {
	r := launch(t, "error", nil)

	if r.res.Outcome != harness.Failed || !strings.Contains(r.res.Reason, "boom") || r.res.SessionID != "s-2" {
		t.Fatalf("résultat = %+v, attendu un échec motivé", r.res)
	}
}

func TestTimeoutIsAFailureThatMentionsTheInterruption(t *testing.T) {
	start := time.Now()
	r := launch(t, "sleep", func(c *claude.Claude) { c.Timeout = 300 * time.Millisecond })

	if r.res.Outcome != harness.Failed || !strings.Contains(r.res.Reason, "durée maximale") {
		t.Fatalf("résultat = %+v, attendu un échec pour durée maximale", r.res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("l'agent n'a pas été interrompu à temps")
	}
}

func TestNoCommitIsAStopForAHuman(t *testing.T) {
	r := launch(t, "noop", nil)

	if r.res.Outcome != harness.Stopped || !strings.Contains(r.res.Reason, "fini") {
		t.Fatalf("résultat = %+v, attendu un arrêt sans commit", r.res)
	}
}

func TestAuthSwitchIsConfigurationOnlyAndForgeTokensNeverReachTheAgent(t *testing.T) {
	for auth, want := range map[claude.Auth][2]string{
		claude.Subscription: {"CLAUDE_CODE_OAUTH_TOKEN=oauth-secret", "ANTHROPIC_API_KEY"},
		claude.APIKey:       {"ANTHROPIC_API_KEY=api-secret", "CLAUDE_CODE_OAUTH_TOKEN"},
	} {
		t.Run(string(auth), func(t *testing.T) {
			r := launch(t, "noop", func(c *claude.Claude) { c.Auth = auth })

			env := r.file(t, "env")
			if !strings.Contains(env, want[0]) || strings.Contains(env, want[1]+"=") {
				t.Fatalf("environnement de l'agent :\n%s\nattendu %s sans %s", env, want[0], want[1])
			}
			if strings.Contains(env, "forge-secret") {
				t.Fatalf("un jeton de forge a atteint l'agent :\n%s", env)
			}
		})
	}
}

func TestMissingCredentialIsAnError(t *testing.T) {
	r := launch(t, "noop", func(c *claude.Claude) {
		c.Auth = claude.APIKey
		c.Env = []string{"PATH=" + os.Getenv("PATH")}
	})

	if r.err == nil || !strings.Contains(r.err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("erreur = %v, attendu la variable manquante", r.err)
	}
}
