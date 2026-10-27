package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

// writeSecret écrit un secret dans un fichier et rend son chemin.
func writeSecret(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEverySecretAcceptsItsVariableOrAFileNeverBoth(t *testing.T) {
	const value = "s3cr3t-value"
	for _, name := range []string{cli.TokenEnv, cli.GitLabTokenEnv, cli.WebhookEnv, "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			for _, c := range []struct {
				name, env, file string
				want, err       string
			}{
				{name: "variable seule", env: value, want: value},
				{name: "fichier seul", file: writeSecret(t, value+"\n"), want: value},
				{name: "les deux", env: value, file: writeSecret(t, value), err: "exclusives"},
				{name: "fichier vide", file: writeSecret(t, ""), want: ""},
				{name: "fichier absent", file: filepath.Join(t.TempDir(), "absent"), err: "illisible"},
				{name: "aucun", want: ""},
			} {
				t.Run(c.name, func(t *testing.T) {
					t.Setenv(name, c.env)
					t.Setenv(name+cli.FileSuffix, c.file)

					got, err := cli.SecretEnv(name)

					if c.err == "" && (err != nil || got != c.want) {
						t.Fatalf("secret = %q, %v ; attendu %q", got, err, c.want)
					}
					if c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err) || !strings.Contains(err.Error(), name+cli.FileSuffix)) {
						t.Fatalf("erreur = %v, attendu %q qui nomme %s", err, c.err, name+cli.FileSuffix)
					}
					if err != nil && strings.Contains(err.Error(), value) {
						t.Fatal("la valeur du secret fuit dans l'erreur")
					}
				})
			}
		})
	}
}

func TestStagesReadTheForgeTokenAndWebhookFromFiles(t *testing.T) {
	w := newWorld(t)
	token := writeSecret(t, secret+"\n")
	t.Setenv(cli.TokenEnv, "")
	t.Setenv(cli.TokenEnv+cli.FileSuffix, token)

	code, stdout, stderr := run("select", "--repo", "o/a", "--api-url", w.api, "--git-url", w.remote.Base, "--state", w.state, "--work", w.work)
	if code != 0 || !strings.Contains(stdout, "réservé") {
		t.Fatalf("select : code %d\n%s\n%s", code, stdout, stderr)
	}
	t.Setenv(cli.TokenEnv+cli.FileSuffix, "")
	w.agentStage(t, "commit")
	t.Setenv(cli.TokenEnv, "")
	t.Setenv(cli.TokenEnv+cli.FileSuffix, token)
	t.Setenv(cli.WebhookEnv+cli.FileSuffix, writeSecret(t, "http://127.0.0.1:1/api/webhooks/1/secret"))
	code, stdout, stderr = run("publish", "--api-url", w.api, "--git-url", w.remote.Base, "--ci-timeout", "200ms", "--ci-poll", "1ms", "--ci-settle", "2ms", "--state", w.state, "--work", w.work)

	if code != 0 || len(w.gh.pulls) != 1 {
		t.Fatalf("publish : code %d, PR %d\n%s\n%s", code, len(w.gh.pulls), stdout, stderr)
	}
	if strings.Contains(stdout, "non configurée") {
		t.Fatalf("publish n'a pas lu le webhook dans son fichier :\n%s", stdout)
	}
}

func TestBothFormsOfASecretAreAUsageError(t *testing.T) {
	w := newWorld(t)
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv(cli.TokenEnv+cli.FileSuffix, writeSecret(t, secret))

	code, _, stderr := run("select", "--repo", "o/a", "--api-url", w.api, "--git-url", w.remote.Base, "--state", w.state, "--work", w.work)

	if code != 2 || !strings.Contains(stderr, cli.TokenEnv+cli.FileSuffix) || strings.Contains(stderr, secret) {
		t.Fatalf("code = %d, stderr = %q ; attendu 2, les deux variables nommées, jamais la valeur", code, stderr)
	}
}

func TestAgentReceivesTheModelTokenFromItsFileAndNoFileVariable(t *testing.T) {
	w := newWorld(t)
	w.selectStage(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN"+cli.FileSuffix, writeSecret(t, "oauth-from-file\n"))
	t.Setenv("UNRELATED"+cli.FileSuffix, "/etc/hostname")
	agentEnv(t, w)
	bin, _ := filepath.Abs("../harness/claude/testdata/fake-claude")

	code, stdout, stderr := run("agent", "--claude", bin, "--state", w.state, "--work", w.work)

	if code != 0 || !strings.Contains(stdout, "succeeded") {
		t.Fatalf("agent : code %d\n%s\n%s", code, stdout, stderr)
	}
	b, err := os.ReadFile(filepath.Join(w.log, "env"))
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(b), "\n")
	if !slices.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=oauth-from-file") {
		t.Fatalf("environnement de l'agent sans le jeton lu dans le fichier :\n%s", b)
	}
	if strings.Contains(string(b), cli.FileSuffix+"=") {
		t.Fatalf("une variable %s atteint l'agent :\n%s", cli.FileSuffix, b)
	}
}

func TestAgentRefusesAnyFileFormOfATrustedStageSecret(t *testing.T) {
	for _, name := range append(cli.ForgeSecretEnv(), cli.WebhookEnv) {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.selectStage(t)
			agentEnv(t, w)
			// Même vers un fichier absent.
			t.Setenv(name+cli.FileSuffix, filepath.Join(t.TempDir(), "absent"))

			code, _, stderr := run("agent", "--claude", "/bin/false", "--state", w.state, "--work", w.work)

			if code != 0 || !strings.Contains(stderr, name+cli.FileSuffix) {
				t.Fatalf("code = %d, stderr = %q ; attendu un refus qui nomme la variable", code, stderr)
			}
			t.Setenv(name+cli.FileSuffix, "")
			w.publishStage(t)
			if got := w.lifecycle(); !slices.Equal(got, []string{"ready-for-human"}) {
				t.Fatalf("labels de cycle de vie = %v, attendu [ready-for-human]", got)
			}
		})
	}
}

// agentEnv prépare l'environnement de l'étape agent : aucun jeton de forge,
// le jeton du modèle et la doublure de claude.
func agentEnv(t *testing.T, w *world) {
	t.Helper()
	for _, k := range cli.ForgeSecretEnv() {
		t.Setenv(k, "")
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"+cli.FileSuffix) == "" {
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")
	}
	t.Setenv("FAKE_CLAUDE", "commit")
	t.Setenv("FAKE_CLAUDE_LOG", w.log)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
}
