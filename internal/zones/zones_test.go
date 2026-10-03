package zones_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/zones"
)

// Chaque dossier de testdata est une fixture :
//   - opt-in.yaml : l'adhésion de la base ;
//   - changes : un changement par ligne, « chemin » ou « ancien -> nouveau » ;
//   - base/ et head/ : le contenu des fichiers utiles au check ;
//   - expected : le verdict, puis une ligne « verdict chemin » par constat.
func TestFixtures(t *testing.T) {
	dirs, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		t.Run(d.Name(), func(t *testing.T) {
			dir := filepath.Join("testdata", d.Name())

			o, err := optin.Parse(read(t, dir, "opt-in.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := zones.Check(o, changes(t, dir), fixtureContents(dir))
			if err != nil {
				t.Fatal(err)
			}

			want := strings.TrimSpace(string(read(t, dir, "expected")))
			if got := render(r); got != want {
				t.Fatalf("verdict inattendu\n--- obtenu\n%s\n--- attendu\n%s", got, want)
			}
		})
	}
}

func render(r zones.Report) string {
	lines := []string{r.Verdict.String()}
	for _, f := range r.Findings {
		lines = append(lines, f.Verdict.String()+" "+f.Path)
	}
	return strings.Join(lines, "\n")
}

func changes(t *testing.T, dir string) []zones.Change {
	t.Helper()
	var cs []zones.Change
	for _, l := range strings.Split(strings.TrimSpace(string(read(t, dir, "changes"))), "\n") {
		if old, nu, ok := strings.Cut(l, " -> "); ok {
			cs = append(cs, zones.Change{Path: nu, OldPath: old})
			continue
		}
		cs = append(cs, zones.Change{Path: l})
	}
	return cs
}

type fixtureContents string

func (d fixtureContents) Base(p string) ([]byte, bool, error) { return d.file("base", p) }
func (d fixtureContents) Head(p string) ([]byte, bool, error) { return d.file("head", p) }

func (d fixtureContents) file(side, p string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(string(d), side, p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
