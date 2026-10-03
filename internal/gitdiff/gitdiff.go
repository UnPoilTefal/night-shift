// Package gitdiff lit, dans un dépôt git local, les changements d'une PR et le
// contenu de ses fichiers de part et d'autre, pour le check des zones.
package gitdiff

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/zones"
)

// Repo est un dépôt git local, comparé entre le point où la PR a divergé de
// sa base (merge-base) et sa tête.
type Repo struct {
	dir       string
	mergeBase string
	head      string
}

// Open résout le merge-base entre base et head dans le dépôt dir.
func Open(dir, base, head string) (*Repo, error) {
	for _, rev := range []string{base, head} {
		if err := validRev(rev); err != nil {
			return nil, err
		}
	}
	mb, err := git(dir, "merge-base", base, head)
	if err != nil {
		return nil, fmt.Errorf("merge-base introuvable entre %s et %s : %w", base, head, err)
	}
	return &Repo{dir: dir, mergeBase: strings.TrimSpace(string(mb)), head: head}, nil
}

// Changes liste les fichiers modifiés par la PR, renommages compris.
func (r *Repo) Changes() ([]zones.Change, error) {
	out, err := git(r.dir, "diff", "--name-status", "-z", "-M", r.mergeBase, r.head)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var cs []zones.Change
	for i := 0; i < len(fields) && fields[0] != ""; {
		status := fields[i]
		switch {
		case strings.HasPrefix(status, "R") && i+2 < len(fields):
			cs = append(cs, zones.Change{OldPath: fields[i+1], Path: fields[i+2]})
			i += 3
		case strings.HasPrefix(status, "C") && i+2 < len(fields):
			cs = append(cs, zones.Change{Path: fields[i+2]})
			i += 3
		case i+1 < len(fields):
			cs = append(cs, zones.Change{Path: fields[i+1]})
			i += 2
		default:
			return nil, fmt.Errorf("sortie de git diff inattendue : %q", out)
		}
	}
	return cs, nil
}

// Base rend le contenu d'un fichier au merge-base.
func (r *Repo) Base(path string) ([]byte, bool, error) { return show(r.dir, r.mergeBase, path) }

// Head rend le contenu d'un fichier à la tête de la PR.
func (r *Repo) Head(path string) ([]byte, bool, error) { return show(r.dir, r.head, path) }

// File rend le contenu d'un fichier à une révision quelconque.
func (r *Repo) File(rev, path string) ([]byte, bool, error) { return show(r.dir, rev, path) }

func show(dir, rev, path string) ([]byte, bool, error) {
	if err := validRev(rev); err != nil {
		return nil, false, err
	}
	if _, err := git(dir, "cat-file", "-e", rev+":"+path); err != nil {
		return nil, false, nil
	}
	data, err := git(dir, "show", rev+":"+path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// validRev refuse une révision vide ou qui commencerait par « - » : git la
// lirait comme une option.
func validRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("révision invalide : %q", rev)
	}
	return nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // G204 : binaire fixe, révisions validées par validRev
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s : %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return out, nil
}
