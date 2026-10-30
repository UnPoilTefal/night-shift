package proofcli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/proofcli"
)

const secret = "ghp_cli-secret-token"

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = proofcli.Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestProofReplayUsageErrors(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	t.Setenv(cli.ControlTokenEnv, "control-token")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"sans sous-commande":   {nil, "night-shift-proof replay"},
		"sans dépôt":           {[]string{"replay", "--compose-file", "c.yaml"}, "--repo"},
		"sans fichier Compose": {[]string{"replay", "--repo", "o/sandbox"}, "--compose-file"},
		"cas inconnu":          {[]string{"replay", "--repo", "o/sandbox", "--compose-file", "c.yaml", "--case", "E42"}, "E42"},
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
	code, _, stderr := run("replay", "--repo", "o/sandbox", "--compose-file", "c.yaml")
	if code != 2 || !strings.Contains(stderr, cli.ControlTokenEnv) {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}
