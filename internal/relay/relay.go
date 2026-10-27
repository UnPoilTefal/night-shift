// Package relay fixe le contrat du relais v1 (#108, #109) : les étapes d'une
// passe tournent en services permanents et attendent leur tour, au lieu
// d'être lancées dans l'ordre par un orchestrateur. Il fait partie de
// l'interface publique sous semver (ADR 0013).
//
//   - L'agent ne parle qu'aux volumes : il attend un marqueur de tour
//     (TurnFile) dans son volume de tâche, en lecture seule pour lui, et
//     écrit sa fin (DoneFile) dans sa sortie. Ce second marqueur vient de
//     l'agent : il est lu en donnée non fiable, et peut être forgé.
//   - Les étapes de confiance parlent au service de contrôle en HTTP, sur un
//     réseau où l'agent n'est pas : select y réclame une passe (ClaimPath),
//     et chaque étape y rapporte ses événements (EventsPath).
package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Noms des marqueurs : TurnFile dans le volume de tâche (state, state2),
// DoneFile dans la sortie de l'agent (out/).
const (
	TurnFile = "turn.json"
	DoneFile = "done.json"
)

// maxMarker borne la lecture d'un marqueur.
const maxMarker = 4 << 10

// Turn est le marqueur d'un tour : select l'écrit pour le premier, publish
// pour la relance, en dernier, après la tâche et le rapport.
type Turn struct {
	Pass  string `json:"pass"`
	Round int    `json:"round"`
	// Ticket vaut false si aucun ticket n'est réservé : l'agent ne fait rien
	// et publish publie seul le digest.
	Ticket bool `json:"ticket"`
	// Deadline est l'échéance du tour : début du tour plus --round-timeout.
	// Passée, l'agent ignore le marqueur et publish avance comme pour un
	// agent sans résultat.
	Deadline time.Time `json:"deadline"`
}

// Expired dit si l'échéance du tour est passée à now.
func (t Turn) Expired(now time.Time) bool { return !now.Before(t.Deadline) }

// Done est le marqueur de fin de l'agent, écrit après result.json.
type Done struct {
	Pass  string `json:"pass"`
	Round int    `json:"round"`
}

// Matches dit si la fin annoncée est celle du tour t.
func (d Done) Matches(t Turn) bool { return d.Pass == t.Pass && d.Round == t.Round }

// WriteTurn écrit le marqueur de tour dans dir par renommage atomique :
// l'agent ne voit jamais un marqueur à moitié écrit.
func WriteTurn(dir string, t Turn) error { return writeAtomic(dir, TurnFile, t) }

// ReadTurn relit le marqueur de tour ; ok vaut false s'il n'y en a pas.
func ReadTurn(dir string) (t Turn, ok bool, err error) {
	ok, err = read(filepath.Join(dir, TurnFile), &t)
	return t, ok, err
}

// WriteDone écrit le marqueur de fin de l'agent dans sa sortie.
func WriteDone(out string, d Done) error { return writeAtomic(out, DoneFile, d) }

// ReadDone relit le marqueur de fin de l'agent, en donnée non fiable :
// fichier ordinaire seulement, sans suivre de lien, taille bornée.
func ReadDone(out string) (d Done, ok bool, err error) {
	ok, err = read(filepath.Join(out, DoneFile), &d)
	return d, ok, err
}

func writeAtomic(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+name+"-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil { // #nosec G302 -- marqueur sans secret, lu par l'étape suivante
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

func read(path string, v any) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxMarker {
		return true, fmt.Errorf("%s : fichier ordinaire d'au plus %d octets attendu", filepath.Base(path), maxMarker)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- chemin construit ici, lien refusé
	if err != nil {
		return true, fmt.Errorf("%s : ouverture refusée", filepath.Base(path))
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxMarker))
	if err != nil {
		return true, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, fmt.Errorf("%s illisible", filepath.Base(path))
	}
	return true, nil
}
