package proofcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

func TestImageVersion(t *testing.T) {
	onTag := func(tag string) func() (string, error) {
		return func() (string, error) { return tag, nil }
	}
	offTag := func() (string, error) { return "", errors.New("pas de tag") }
	for name, tc := range map[string]struct {
		image string
		tag   func() (string, error)
		want  string
		fails bool
	}{
		"déduite du tag":        {tag: onTag("v0.5.0"), want: "0.5.0"},
		"pré-release":           {tag: onTag("v0.5.0-rc.1"), want: "0.5.0-rc.1"},
		"hors d'un tag":         {tag: offTag, fails: true},
		"tag autre":             {tag: onTag("essai"), fails: true},
		"image explicite":       {image: imageRepo + ":dev", tag: offTag, want: "dev"},
		"elle prime sur le tag": {image: imageRepo + ":1.2.3", tag: onTag("v0.5.0"), want: "1.2.3"},
		"sans étiquette":        {image: imageRepo, tag: offTag, fails: true},
		"autre image":           {image: "example.com/night-shift:1", tag: offTag, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := imageVersion(tc.image, tc.tag)
			if (err != nil) != tc.fails || got != tc.want {
				t.Fatalf("imageVersion = %q, %v", got, err)
			}
		})
	}
}

func TestEnvironmentIsNeverNamedAfterAUsageDeployment(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "night-shift")
	t.Setenv("COMPOSE_FILE", "autre.yaml")
	t.Setenv("NIGHT_SHIFT_SECRETS_DIR", "/usage/secrets")
	t.Setenv("NIGHT_SHIFT_GITHUB_TOKEN", "ghp_usage")
	e := Environment{Root: "/r", Secrets: "/r/test/proof/secrets", Repo: "o/s", AgentConfig: "o/c", Forge: "github", Version: "1.2.3", AdminPort: "18081"}
	compose := strings.Join(e.Compose(), " ")
	if !strings.Contains(compose, "--project-name night-shift-proof ") {
		t.Fatalf("commande : %s", compose)
	}
	env := e.Env()
	for _, kv := range env {
		if strings.HasPrefix(kv, "COMPOSE_") || strings.Contains(kv, "/usage/") || strings.Contains(kv, "ghp_usage") {
			t.Fatalf("héritée de l'appelant : %s", kv)
		}
	}
	for _, want := range []string{"NIGHT_SHIFT_REPO=o/s", "NIGHT_SHIFT_SCHEDULE=" + farSchedule, "NIGHT_SHIFT_INSTANCE=", "NIGHT_SHIFT_ADMIN_PORT=18081", "NIGHT_SHIFT_SECRETS_DIR=/r/test/proof/secrets"} {
		if !slices.Contains(env, want) {
			t.Fatalf("%s absent de %v", want, env)
		}
	}
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const goodOptIn = "version: 1\ntrustLevel: {ticketsPerPass: 1, pullRequests: draft}\nforbiddenZones: [\".github/**\", \".gitlab-ci.yml\"]\n"

func TestMissingSecretsNamesEachOne(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "forge_token", "x")
	write(t, dir, "model_token", "")
	write(t, dir, "discord_webhook", "")
	got := strings.Join(missingSecrets(dir), "\n")
	for _, want := range []string{"secret vide : model_token", "secret absent : control_token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q absent de %q", want, got)
		}
	}
	if strings.Contains(got, "forge_token") || strings.Contains(got, "discord_webhook") {
		t.Fatalf("signalé à tort : %q", got)
	}
}

func TestSandboxProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		forge string
		files map[string]string
		want  []string
	}{
		"conforme sur GitHub": {"github", map[string]string{".night-shift/opt-in.yaml": goodOptIn, ".github/workflows/ci.yml": "run: test ! -f ci-red"}, nil},
		"conforme sur GitLab": {"gitlab", map[string]string{".night-shift/opt-in.yaml": goodOptIn, ".gitlab-ci.yml": "script: test ! -f ci-red"}, nil},
		"dépôt vide":          {"github", nil, []string{"pas d'adhésion", "ci-red"}},
		"adhésion invalide":   {"github", map[string]string{".night-shift/opt-in.yaml": "version: 42", ".github/workflows/ci.yml": "ci-red"}, []string{"adhésion invalide"}},
		"CI permise":          {"github", map[string]string{".night-shift/opt-in.yaml": "version: 1\ntrustLevel: {ticketsPerPass: 1, pullRequests: draft}\n", ".github/workflows/ci.yml": "ci-red"}, []string{"n'interdit pas"}},
		"CI sans ci-red":      {"github", map[string]string{".night-shift/opt-in.yaml": goodOptIn, ".github/workflows/ci.yml": "run: test"}, []string{"ci-red"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range tc.files {
				write(t, dir, path, content)
			}
			got := sandboxProblems(dir, tc.forge)
			if len(got) != len(tc.want) {
				t.Fatalf("problèmes : %q, attendu %q", got, tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Fatalf("problème %d : %q, attendu %q", i, got[i], want)
				}
			}
		})
	}
}

func TestAgentConfigProblems(t *testing.T) {
	good := t.TempDir()
	write(t, good, "agent.yaml", "version: 1\ndeniedTools:\n  - \"Bash(make release*)\"\n")
	if got := agentConfigProblems(good); got != nil {
		t.Fatalf("configuration conforme refusée : %q", got)
	}
	other := t.TempDir()
	write(t, other, "agent.yaml", "version: 1\ndeniedTools:\n  - \"Bash(npm publish*)\"\n")
	if got := agentConfigProblems(other); len(got) != 1 || !strings.Contains(got[0], e8Refusal) {
		t.Fatalf("refus manquant non signalé : %q", got)
	}
}

func TestCheckRepositoriesReadsBothClones(t *testing.T) {
	sandbox, config := testgit.New(t), testgit.New(t)
	sandbox.Write(".night-shift/opt-in.yaml", goodOptIn)
	sandbox.Write(".github/workflows/ci.yml", "run: test")
	sandbox.Commit("adhésion")
	config.Write("agent.yaml", "version: 1\n")
	config.Commit("configuration")
	remotes := map[string]*testgit.Remote{"o/s": testgit.NewRemote(t, "o/s", sandbox), "o/c": testgit.NewRemote(t, "o/c", config)}
	access := func(repo string) gitrepo.Remote {
		return gitrepo.Remote{GitAccess: forge.GitAccess{URL: "file://" + remotes[repo].Dir()}}
	}
	got, err := checkRepositories(context.Background(), Environment{Repo: "o/s", AgentConfig: "o/c", Forge: "github"}, access)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	if len(got) != 2 || !strings.Contains(joined, "ci-red") || !strings.Contains(joined, e8Refusal) {
		t.Fatalf("problèmes : %q", got)
	}
}
