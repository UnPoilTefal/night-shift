package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

func TestControlServeUsageErrors(t *testing.T) {
	history := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("jeton-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args       []string
		env, file  string
		wantStderr string
	}{
		"sans historique":        {args: []string{"control", "serve"}, wantStderr: "--history"},
		"échéance invalide":      {args: []string{"control", "serve", "--history", history, "--schedule", "tous les soirs"}, wantStderr: "--schedule"},
		"administration nue":     {args: []string{"control", "serve", "--history", history, "--admin-listen", "127.0.0.1:0"}, wantStderr: cli.ControlTokenEnv},
		"jeton sous deux formes": {args: []string{"control", "serve", "--history", history, "--admin-listen", "127.0.0.1:0"}, env: "jeton-secret", file: tokenFile, wantStderr: "exclusives"},
		"sous-commande inconnue": {args: []string{"control", "stop"}, wantStderr: "inconnue"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(cli.ControlTokenEnv, c.env)
			t.Setenv(cli.ControlTokenEnv+"_FILE", c.file)

			code, _, stderr := run(c.args...)

			if code != 2 || !strings.Contains(stderr, c.wantStderr) || strings.Contains(stderr, "jeton-secret") {
				t.Fatalf("code = %d, stderr = %q ; attendu 2, %q, sans le jeton", code, stderr, c.wantStderr)
			}
		})
	}
}

func TestControlTriggerLeavesARequestForTheService(t *testing.T) {
	dir := t.TempDir()

	code, stdout, stderr := run("control", "trigger", "--requests", dir)

	if code != 0 || !strings.Contains(stdout, "passe demandée") {
		t.Fatalf("code = %d\n%s\n%s", code, stdout, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("demandes = %v, attendu une", entries)
	}
}
