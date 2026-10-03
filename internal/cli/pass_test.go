package cli_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

const secret = "ghp_cli-secret-token"

func TestPassRequiresToken(t *testing.T) {
	t.Setenv(cli.TokenEnv, "")
	code, _, stderr := run("pass", "--repo", "o/a")
	if code != 2 || !strings.Contains(stderr, cli.TokenEnv) {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestPassRejectsInvalidRepo(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	if code, _, _ := run("pass", "--repo", "o/../../x"); code != 2 {
		t.Fatalf("code = %d, attendu 2", code)
	}
}

func TestPassReportsAndNeverPrintsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/contents/.night-shift/opt-in.yaml") && strings.HasPrefix(r.URL.Path, "/repos/o/a/"):
			_, _ = io.WriteString(w, "version: 1\ntrustLevel: {ticketsPerPass: 1, pullRequests: draft}\n")
		case r.URL.Path == "/repos/o/a/issues":
			_, _ = io.WriteString(w, "[]")
		case r.URL.Path == "/repos/o/b/contents/.night-shift/opt-in.yaml":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom "+secret)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv(cli.TokenEnv, secret)

	code, stdout, stderr := run("pass", "--repo", "o/a,o/b,o/c", "--api-url", srv.URL)

	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"o/a : eligible", "o/b : unreachable", "o/c : no-opt-in", "aucun ticket prêt éligible"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, attendu %q", stdout, want)
		}
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Fatalf("le jeton apparaît dans la sortie :\n%s\n%s", stdout, stderr)
	}
}
