package cli_test

import (
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

const optIn = `version: 1
trustLevel:
  ticketsPerPass: 1
  pullRequests: draft
forbiddenZones: [".github/**"]
sensitiveZones:
  - path: "deploy/**"
`

// agentPR crée un dépôt dont main porte l'adhésion donnée, et une branche
// agent/1-demo qui modifie les fichiers donnés.
func agentPR(t *testing.T, baseOptIn string, files ...string) *testgit.Repo {
	t.Helper()
	repo := testgit.New(t)
	repo.Write("README.md", "base\n")
	if baseOptIn != "" {
		repo.Write(".night-shift/opt-in.yaml", baseOptIn)
	}
	repo.Commit("base")
	repo.Git("checkout", "-q", "-b", "agent/1-demo")
	if len(files) == 0 {
		return repo
	}
	for _, f := range files {
		repo.Write(f, "changed\n")
	}
	repo.Commit("agent")
	return repo
}

func zones(repo *testgit.Repo, extra ...string) (int, string, string) {
	return run(append([]string{"zones", "--repo", repo.Dir, "--base", "main", "--head", "agent/1-demo"}, extra...)...)
}

func TestZonesPass(t *testing.T) {
	code, stdout, stderr := zones(agentPR(t, optIn, "README.md"))
	if code != 0 || !strings.HasPrefix(stdout, "verdict: pass") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestZonesFlagSucceedsButReports(t *testing.T) {
	code, stdout, stderr := zones(agentPR(t, optIn, "deploy/app.yaml"))
	if code != 0 || !strings.HasPrefix(stdout, "verdict: flag") || !strings.Contains(stdout, "deploy/app.yaml") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestZonesBlockFails(t *testing.T) {
	code, stdout, _ := zones(agentPR(t, optIn, ".github/workflows/ci.yml"))
	if code != 1 || !strings.HasPrefix(stdout, "verdict: block") {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
}

func TestZonesUsesBaseOptInNotPR(t *testing.T) {
	// La PR retire la zone interdite de l'adhésion et touche .github :
	// l'adhésion de la base fait foi, et sa modification bloque aussi.
	repo := agentPR(t, optIn)
	repo.Write(".night-shift/opt-in.yaml", strings.Replace(optIn, `".github/**"`, `"nothing/**"`, 1))
	repo.Write(".github/workflows/ci.yml", "changed\n")
	repo.Commit("loosen")

	code, stdout, _ := zones(repo)
	if code != 1 || !strings.Contains(stdout, "block .github/workflows/ci.yml") || !strings.Contains(stdout, "block .night-shift/opt-in.yaml") {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
}

func TestZonesInvalidOptInFailsExplicitly(t *testing.T) {
	code, _, stderr := zones(agentPR(t, strings.Replace(optIn, "draft", "merged", 1), "README.md"))
	if code != 1 || !strings.Contains(stderr, "pullRequests") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestZonesMissingOptInFails(t *testing.T) {
	code, _, stderr := zones(agentPR(t, "", "README.md"))
	if code != 1 || !strings.Contains(stderr, "pas d'adhésion") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestZonesSkipsNonAgentPR(t *testing.T) {
	code, stdout, _ := zones(agentPR(t, optIn, ".github/workflows/ci.yml"), "--head-ref", "feat/human")
	if code != 0 || !strings.Contains(stdout, "non applicable") {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
}

func TestZonesRequiresBase(t *testing.T) {
	code, _, _ := run("zones")
	if code != 2 {
		t.Fatalf("code = %d, attendu 2", code)
	}
}
