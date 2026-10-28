package cli_test

import (
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

func TestProofReplayUsageErrors(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv(cli.ControlTokenEnv, "control-token")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"sans sous-commande":   {[]string{"proof"}, "proof replay"},
		"sans dépôt":           {[]string{"proof", "replay", "--compose-file", "c.yaml"}, "--repo"},
		"sans fichier Compose": {[]string{"proof", "replay", "--repo", "o/sandbox"}, "--compose-file"},
		"cas inconnu":          {[]string{"proof", "replay", "--repo", "o/sandbox", "--compose-file", "c.yaml", "--case", "E42"}, "E42"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := run(tc.args...)
			if code != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
		})
	}
}

func TestProofReplayNeedsTheControlToken(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv(cli.ControlTokenEnv, "")
	code, _, stderr := run("proof", "replay", "--repo", "o/sandbox", "--compose-file", "c.yaml")
	if code != 2 || !strings.Contains(stderr, cli.ControlTokenEnv) {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}
