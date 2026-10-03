// Package testgit construit des dépôts git jetables pour les tests.
package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Repo est un dépôt git temporaire, supprimé à la fin du test.
type Repo struct {
	t   *testing.T
	Dir string
}

// New initialise un dépôt dont la branche par défaut est main.
func New(t *testing.T) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir()}
	r.Git("init", "-q", "-b", "main")
	return r
}

// Write crée ou remplace un fichier, dossiers parents compris.
func (r *Repo) Write(name, content string) {
	r.t.Helper()
	p := filepath.Join(r.Dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// Commit indexe tout et commite.
func (r *Repo) Commit(msg string) {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", msg)
}

// Git exécute une commande git dans le dépôt et échoue le test en cas d'erreur.
func (r *Repo) Git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v : %v\n%s", args, err, out)
	}
}
