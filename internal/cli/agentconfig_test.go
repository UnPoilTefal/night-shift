package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

// withAgentConfig publie le dépôt de configuration o/agent-config à côté de
// o/a, avec agent.yaml, puis le passe à select ; il rend sa révision.
func (w *world) withAgentConfig(t *testing.T, agentYAML string) string {
	t.Helper()
	src := testgit.New(t)
	src.Write("agent.yaml", agentYAML)
	src.Write("skills/review/SKILL.md", "---\nname: review\n---\n")
	src.Write("README.md", "hors configuration\n")
	src.Commit("config")
	bare := filepath.Join(w.remote.Base, "o", "agent-config.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init : %v\n%s", err, out)
	}
	src.Git("push", "-q", bare, "main")
	w.selectArgs = []string{"--agent-config", "o/agent-config"}
	return src.Out("rev-parse", "HEAD")
}

func deniedArg(t *testing.T, w *world) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.log, "args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i, a := range args {
		if a == "--disallowedTools" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("arguments = %q, attendu --disallowedTools", args)
	return ""
}

func TestAgentConfigReachesBothRoundsAndItsRevisionTheTicket(t *testing.T) {
	w := newWorld(t)
	w.gh.red = 1
	rev := w.withAgentConfig(t, "version: 1\ndeniedTools: [\"Bash(npm publish*)\"]\n")

	w.selectStage(t)
	if _, err := os.Stat(filepath.Join(w.state, "agent-config", "README.md")); !os.IsNotExist(err) {
		t.Fatalf("le reste du dépôt de configuration est recopié : %v", err)
	}
	w.agentStage(t, "commit")
	if !strings.Contains(deniedArg(t, w), "Bash(npm publish*)") {
		t.Fatal("premier tour sans les refus de la configuration")
	}
	w.publishStage(t, "--next-state", w.state2, "--next-work", w.work2)
	w.agentOn(t, "fix", w.state2, w.work2)
	if d := deniedArg(t, w); !strings.Contains(d, "Bash(npm publish*)") || !strings.Contains(d, "Bash(git push*)") {
		t.Fatalf("refus de la relance = %q, attendu les refus fixes et ceux de la configuration", d)
	}
	w.publishStage(t, "--follow-up", "--state", w.state2, "--work", w.work2)

	last := w.gh.comments[len(w.gh.comments)-1]
	if !strings.Contains(last, "Configuration de l'agent : o/agent-config@"+rev) {
		t.Fatalf("commentaire final = %q, attendu la révision %s", last, rev)
	}
}

func TestInvalidAgentConfigReservesNoTicket(t *testing.T) {
	w := newWorld(t)
	w.withAgentConfig(t, "version: 1\nallowedTools: [\"Bash(*)\"]\n")

	stdout := w.selectStage(t)

	if !strings.Contains(stdout, "aucun ticket prêt éligible") || len(w.lifecycle()) != 1 {
		t.Fatalf("select = %q, labels = %v ; attendu aucune réservation", stdout, w.lifecycle())
	}
	if _, err := os.Stat(filepath.Join(w.state, "task.json")); !os.IsNotExist(err) {
		t.Fatal("tâche écrite malgré une configuration invalide")
	}
}

func TestAgentConfigRepoCannotBeACandidate(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	code, _, stderr := run("select", "--repo", "o/a", "--agent-config", "o/a", "--state", t.TempDir(), "--work", t.TempDir())
	if code != 2 || !strings.Contains(stderr, "configuration de l'agent") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}
