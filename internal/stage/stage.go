// Package stage fixe le passage de relais entre les trois conteneurs d'une
// passe du palier solo : sélection, agent, Publication. Deux volumes :
//
//   - State, écrit par la sélection et monté en lecture seule ailleurs,
//     porte la tâche (ticket réservé, brief, commit de base) : l'agent ne peut
//     pas changer le ticket que la Publication rendra ;
//   - Work porte le clone (repo/) que l'agent modifie, et sa sortie (out/) :
//     result.json et la série patches/NNNN.patch.
//
// Tout ce qui vient de Work est écrit par l'agent : ReadResult le traite en
// donnée non fiable (fichiers ordinaires seulement, tailles bornées).
package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/harness"
)

// Bornes de la sortie de l'agent.
const (
	MaxPatches     = 200
	MaxPatchBytes  = 10 << 20
	MaxSeriesBytes = 20 << 20
	MaxReason      = 4000
)

// Dirs désigne les deux volumes partagés.
type Dirs struct {
	State, Work string
}

// RepoDir est le clone du dépôt cible.
func (d Dirs) RepoDir() string { return filepath.Join(d.Work, "repo") }

// OutDir est la sortie de l'agent, hors du clone.
func (d Dirs) OutDir() string { return filepath.Join(d.Work, "out") }

func (d Dirs) taskFile() string   { return filepath.Join(d.State, "task.json") }
func (d Dirs) resultFile() string { return filepath.Join(d.OutDir(), "result.json") }
func (d Dirs) patchDir() string   { return filepath.Join(d.OutDir(), "patches") }

// WriteTask enregistre la tâche réservée par la sélection.
func (d Dirs) WriteTask(t harness.Task) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.taskFile(), b, 0o644) // #nosec G306 -- lue par les conteneurs suivants, sans secret
}

// ReadTask relit la tâche ; ok vaut false si la sélection n'a rien réservé.
func (d Dirs) ReadTask() (harness.Task, bool, error) {
	var t harness.Task
	b, err := os.ReadFile(d.taskFile())
	if errors.Is(err, os.ErrNotExist) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, false, fmt.Errorf("tâche illisible : %w", err)
	}
	return t, true, nil
}

type result struct {
	Outcome    harness.Outcome `json:"outcome"`
	Reason     string          `json:"reason"`
	Agent      string          `json:"agent"`
	CostUSD    float64         `json:"costUSD"`
	Duration   time.Duration   `json:"duration"`
	SessionID  string          `json:"sessionID"`
	Transcript string          `json:"transcript"`
}

// WriteResult enregistre le résultat de l'agent et sa série de commits.
func (d Dirs) WriteResult(r harness.Result) error {
	if err := os.MkdirAll(d.patchDir(), 0o755); err != nil { // #nosec G301 -- lu par le conteneur de Publication
		return err
	}
	for i, p := range r.Patches {
		if err := os.WriteFile(filepath.Join(d.patchDir(), fmt.Sprintf("%04d.patch", i+1)), p, 0o644); err != nil { // #nosec G306 -- idem
			return err
		}
	}
	b, err := json.MarshalIndent(result{
		Outcome: r.Outcome, Reason: r.Reason, Agent: r.Agent, CostUSD: r.CostUSD,
		Duration: r.Duration, SessionID: r.SessionID, Transcript: r.Transcript,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.resultFile(), b, 0o644) // #nosec G306 -- idem
}

var patchName = regexp.MustCompile(`^[0-9]{4}\.patch$`)

// ReadResult relit le résultat de l'agent ; ok vaut false si l'agent n'en a
// rendu aucun (interruption, crash).
func (d Dirs) ReadResult() (harness.Result, bool, error) {
	if err := realDir(d.OutDir()); errors.Is(err, os.ErrNotExist) {
		return harness.Result{}, false, nil
	} else if err != nil {
		return harness.Result{}, false, err
	}
	b, err := readRegular(d.resultFile(), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return harness.Result{}, false, nil
	}
	if err != nil {
		return harness.Result{}, false, err
	}
	var r result
	if err := json.Unmarshal(b, &r); err != nil {
		return harness.Result{}, true, fmt.Errorf("résultat de l'agent illisible : %w", err)
	}
	res := harness.Result{
		Outcome: r.Outcome, Reason: truncate(r.Reason, MaxReason), Agent: truncate(r.Agent, 100),
		CostUSD: r.CostUSD, Duration: r.Duration, SessionID: truncate(r.SessionID, 200), Transcript: truncate(r.Transcript, 500),
	}
	if !slices.Contains([]harness.Outcome{harness.Succeeded, harness.NeedsInfo, harness.Stopped, harness.Failed}, res.Outcome) {
		res.Outcome, res.Reason = harness.Failed, fmt.Sprintf("issue %q inconnue rendue par l'agent", truncate(string(r.Outcome), 50))
	}
	if res.Outcome != harness.Succeeded {
		return res, true, nil
	}

	if err := realDir(d.patchDir()); err != nil {
		return res, true, err
	}
	entries, err := os.ReadDir(d.patchDir())
	if err != nil {
		return res, true, err
	}
	if len(entries) > MaxPatches {
		return res, true, fmt.Errorf("série de %d patchs, au plus %d", len(entries), MaxPatches)
	}
	total := 0
	for _, e := range entries {
		if !patchName.MatchString(e.Name()) {
			return res, true, fmt.Errorf("fichier inattendu dans la série : %q", truncate(e.Name(), 100))
		}
		p, err := readRegular(filepath.Join(d.patchDir(), e.Name()), MaxPatchBytes)
		if err != nil {
			return res, true, err
		}
		if total += len(p); total > MaxSeriesBytes {
			return res, true, fmt.Errorf("série de plus de %d octets", MaxSeriesBytes)
		}
		res.Patches = append(res.Patches, p)
	}
	return res, true, nil
}

// realDir vérifie que path est un dossier, et non un lien symbolique.
func realDir(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s n'est pas un dossier", filepath.Base(path))
	}
	return nil
}

// readRegular lit un fichier ordinaire, sans suivre de lien symbolique, d'au
// plus limit octets.
func readRegular(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s n'est pas un fichier ordinaire", filepath.Base(path))
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s dépasse %d octets", filepath.Base(path), limit)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- chemin construit ici, lien refusé
	if err != nil {
		return nil, fmt.Errorf("%s : ouverture refusée", filepath.Base(path))
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
