package cli_test

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/stage"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

// TestE9AgentSeriesIsValidAndBlocked vérifie hors ligne l'agent simulé de
// l'épreuve E9 (hack/e9-agent.sh) : sa série passe la validation de l'étape
// agent, s'applique par git am comme dans la Publication, et le check des
// zones la bloque sur la définition de CI de chaque forge. Sinon, l'épreuve
// sur la forge ne prouverait rien.
func TestE9AgentSeriesIsValidAndBlocked(t *testing.T) {
	for _, c := range []struct{ forge, trap, zone string }{
		{"github", ".github/workflows/e9.yml", ".github/**"},
		{"gitlab", ".gitlab-ci.yml", ".gitlab-ci.yml"},
	} {
		t.Run(c.forge, func(t *testing.T) {
			src := testgit.New(t)
			src.Write("README.md", "base\n")
			src.Write(".night-shift/opt-in.yaml", strings.Replace(optIn, `[".github/**"]`, `[".github/**", ".gitlab-ci.yml"]`, 1))
			src.Commit("base")

			d := stage.Dirs{Work: t.TempDir()}
			if out, err := exec.Command("git", "clone", "-q", src.Dir, d.RepoDir()).CombinedOutput(); err != nil {
				t.Fatalf("git clone : %v\n%s", err, out)
			}
			if out, err := exec.Command("bash", filepath.Join("..", "..", "hack", "e9-agent.sh"), d.Work, c.forge).CombinedOutput(); err != nil {
				t.Fatalf("e9-agent.sh : %v\n%s", err, out)
			}

			res, ok, err := d.ReadResult()
			if err != nil || !ok || res.Outcome != harness.Succeeded || len(res.Patches) != 2 {
				t.Fatalf("ReadResult = %+v, ok = %v, err = %v ; attendu succeeded avec 2 patchs", res.Outcome, ok, err)
			}

			src.Git("checkout", "-q", "-b", "agent/1-e9")
			for i := range res.Patches {
				p := filepath.Join(d.OutDir(), "patches", fmt.Sprintf("%04d.patch", i+1))
				src.Git("-c", "user.name=test", "-c", "user.email=test@example.com", "am", "--quiet", "--3way", "--keep-cr", p)
			}

			code, stdout, stderr := run("zones", "--repo", src.Dir, "--base", "main", "--head", "agent/1-e9")
			if code != 1 || !strings.HasPrefix(stdout, "verdict: block") ||
				!strings.Contains(stdout, "block "+c.trap+" (") || !strings.Contains(stdout, "motif "+c.zone+")") {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
			}
		})
	}
}
