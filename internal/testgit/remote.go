package testgit

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Remote est un dépôt nu servi depuis un dossier, comme une forge :
// <Base>/<propriétaire>/<nom>.git.
type Remote struct {
	t    *testing.T
	Base string
	Repo string
}

// NewRemote crée le dépôt nu repo (propriétaire/nom) et y pousse la branche
// main de src.
func NewRemote(t *testing.T, repo string, src *Repo) *Remote {
	t.Helper()
	r := &Remote{t: t, Base: t.TempDir(), Repo: repo}
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", r.Dir())
	src.Git("push", "-q", r.Dir(), "main")
	return r
}

// Dir est le chemin du dépôt nu.
func (r *Remote) Dir() string { return filepath.Join(r.Base, r.Repo+".git") }

// Out exécute une commande git dans le dépôt nu et rend sa sortie.
func (r *Remote) Out(args ...string) string {
	r.t.Helper()
	return run(r.t, r.Dir(), "git", args...)
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v : %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Out exécute une commande git dans le dépôt et rend sa sortie.
func (r *Repo) Out(args ...string) string {
	r.t.Helper()
	return run(r.t, r.Dir, "git", args...)
}
