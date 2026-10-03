package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/version"
)

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = cli.Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionPrintsInjectedVersion(t *testing.T) {
	previous := version.Version
	version.Version = "v1.2.3-test"
	t.Cleanup(func() { version.Version = previous })

	code, stdout, stderr := run("version")

	if code != 0 {
		t.Fatalf("code de sortie = %d, attendu 0 (stderr : %q)", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "v1.2.3-test" {
		t.Fatalf("sortie = %q, attendu %q", got, "v1.2.3-test")
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	_, stdout, _ := run("version")

	if got := strings.TrimSpace(stdout); got != "dev" {
		t.Fatalf("sortie = %q, attendu %q sans injection au build", got, "dev")
	}
}

func TestNoCommandPrintsUsageAndFails(t *testing.T) {
	code, _, stderr := run()

	if code != 2 {
		t.Fatalf("code de sortie = %d, attendu 2", code)
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("stderr = %q, attendu un message d'usage", stderr)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	code, _, stderr := run("frobnicate")

	if code != 2 {
		t.Fatalf("code de sortie = %d, attendu 2", code)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Fatalf("stderr = %q, attendu le nom de la commande inconnue", stderr)
	}
}
