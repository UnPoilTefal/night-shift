package proofcli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/proofcli"
)

func run(args ...string) (code int, stderr string) {
	var out, errOut bytes.Buffer
	code = proofcli.Run(args, &out, &errOut)
	return code, errOut.String()
}

func TestUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"sans commande":                  {nil, "night-shift-proof <commande>"},
		"commande inconnue":              {[]string{"rm"}, "commande inconnue"},
		"sans dépôt":                     {[]string{"up", "--agent-config", "o/conf"}, "--repo"},
		"sans configuration":             {[]string{"replay", "--repo", "o/sandbox"}, "--agent-config"},
		"deux fois le dépôt":             {[]string{"up", "--repo", "o/a", "--agent-config", "o/a"}, "deux dépôts"},
		"cas inconnu":                    {[]string{"replay", "--repo", "o/a", "--agent-config", "o/b", "--case", "E42"}, "E42"},
		"le projet n'est pas une option": {[]string{"up", "--compose-project", "night-shift"}, "compose-project"},
		"ni les fichiers":                {[]string{"replay", "--compose-file", "c.yaml"}, "compose-file"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := run(tc.args...)
			if code != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
		})
	}
}

func TestDockerHostIsRefused(t *testing.T) {
	t.Setenv("DOCKER_HOST", "ssh://prete")
	code, stderr := run("up", "--repo", "o/a", "--agent-config", "o/b")
	if code != 2 || !strings.Contains(stderr, "DOCKER_HOST") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}
