package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// passName est la forme d'un identifiant de passe, seul nom de fichier de
// l'historique.
var passName = regexp.MustCompile(`^pass-[0-9]{8}T[0-9]{6}Z$`)

// store est l'historique : un fichier JSON par passe close, <id>.json.
type store struct{ dir string }

func (s store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// ids rend les passes de l'historique, de la plus ancienne à la plus
// récente.
func (s store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && passName.MatchString(id) && e.Type().IsRegular() {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// write écrit l'historique d'une passe par renommage atomique.
func (s store) write(r Record) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ") // #nosec G117 -- "pass" est l'identifiant de la passe, pas un mot de passe
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".pass-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path(r.Pass))
}

// read relit l'historique d'une passe ; ok vaut false si elle n'y est pas.
func (s store) read(id string) (Record, bool, error) {
	var r Record
	if !passName.MatchString(id) {
		return r, false, nil
	}
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, false, fmt.Errorf("historique de %s illisible : %w", id, err)
	}
	return r, true, nil
}

// Purge supprime de l'historique et de l'archive des transcripts les
// fichiers modifiés avant cutoff, et seulement eux, puis les dossiers que
// cette purge a vidés. Un dossier vide est ignoré.
func Purge(cutoff time.Time, dirs ...string) error {
	var errs []error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := purge(cutoff, dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// purge purge un dossier à travers os.Root : un lien posé dans l'archive ne
// mène jamais la purge hors du dossier.
func purge(cutoff time.Time, dir string) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	emptied := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.ModTime().Before(cutoff) {
			if err := root.Remove(path); err != nil {
				return err
			}
			emptied[filepath.Dir(path)] = true
		}
		return nil
	})
	for d := range emptied {
		if d != "." {
			// Échoue sans effet si le dossier garde des fichiers récents.
			_ = root.Remove(d)
		}
	}
	return err
}
