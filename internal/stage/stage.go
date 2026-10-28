// Package stage fixe le passage de relais entre les trois conteneurs d'une
// passe du palier solo : sélection, agent, Publication. Deux volumes :
//
//   - State, écrit par la sélection et monté en lecture seule ailleurs,
//     porte la tâche (ticket réservé, brief, commit de base) et le rapport de
//     la sélection : l'agent ne peut changer ni le ticket que la Publication
//     rendra, ni le digest qu'elle publiera ;
//   - Work porte le clone (repo/) que l'agent modifie, et sa sortie (out/) :
//     result.json, la série patches/NNNN.patch et le transcript de la
//     session, que les étapes de confiance archivent (ArchiveTranscript).
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
	"github.com/UnPoilTefal/night-shift/internal/pass"
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

// AgentConfigDir est la configuration de l'agent, recopiée par la
// sélection depuis son dépôt et en lecture seule pour l'agent.
func (d Dirs) AgentConfigDir() string { return filepath.Join(d.State, "agent-config") }

// OutDir est la sortie de l'agent, hors du clone.
func (d Dirs) OutDir() string { return filepath.Join(d.Work, "out") }

func (d Dirs) taskFile() string   { return filepath.Join(d.State, "task.json") }
func (d Dirs) reportFile() string { return filepath.Join(d.State, "report.json") }
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
// Une tâche écrite sans dépôt cible, par une version précédente, vise le
// dépôt de son ticket.
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
	if t.TargetRepo == "" {
		t.TargetRepo = t.Ticket.Repo
	}
	return t, true, nil
}

// WriteReport enregistre le rapport de la sélection, que la Publication
// complète pour publier le digest.
func (d Dirs) WriteReport(r pass.Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.reportFile(), b, 0o644) // #nosec G306 -- lu par la Publication, sans secret
}

// ReadReport relit le rapport de la sélection ; ok vaut false si elle n'en a
// écrit aucun.
func (d Dirs) ReadReport() (pass.Report, bool, error) {
	var r pass.Report
	b, err := os.ReadFile(d.reportFile())
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, false, fmt.Errorf("rapport de la sélection illisible : %w", err)
	}
	return r, true, nil
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
		CostUSD: r.CostUSD, Duration: r.Duration, SessionID: truncate(r.SessionID, 200),
	}
	// Le transcript n'a qu'un nom possible : aucun chemin rendu par l'agent
	// n'est suivi.
	if r.Transcript == harness.TranscriptFile {
		res.Transcript = r.Transcript
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

// CopyOut copie la sortie de l'agent dans dst, avec les bornes de
// ReadResult, et rend les volumes où la lire : la tâche reste celle de d, la
// sortie est la copie. Ce que l'agent change dans sa sortie après la copie
// n'a plus d'effet : la Publication ne valide et ne pousse que la copie.
// Sans sortie, la copie est vide et ReadResult n'y trouve aucun résultat.
func (d Dirs) CopyOut(dst string) (Dirs, error) {
	c := Dirs{State: d.State, Work: dst}
	if err := realDir(d.OutDir()); errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	if err := os.MkdirAll(c.patchDir(), 0o700); err != nil {
		return c, err
	}
	if err := copyRegular(d.resultFile(), c.resultFile(), 1<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	transcript := filepath.Join(d.OutDir(), harness.TranscriptFile)
	if err := copyRegular(transcript, filepath.Join(c.OutDir(), harness.TranscriptFile), harness.MaxTranscriptBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err := realDir(d.patchDir()); errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	entries, err := os.ReadDir(d.patchDir())
	if err != nil {
		return c, err
	}
	if len(entries) > MaxPatches {
		return c, fmt.Errorf("série de %d patchs, au plus %d", len(entries), MaxPatches)
	}
	var total int64
	for _, e := range entries {
		if !patchName.MatchString(e.Name()) {
			return c, fmt.Errorf("fichier inattendu dans la série : %q", truncate(e.Name(), 100))
		}
		n, err := copyCounted(filepath.Join(d.patchDir(), e.Name()), filepath.Join(c.patchDir(), e.Name()), MaxPatchBytes)
		if err != nil {
			return c, err
		}
		if total += n; total > MaxSeriesBytes {
			return c, fmt.Errorf("série de plus de %d octets", MaxSeriesBytes)
		}
	}
	return c, nil
}

// copyRegular copie un fichier ordinaire d'au plus limit octets, sans suivre
// de lien symbolique.
func copyRegular(src, dst string, limit int64) error {
	_, err := copyCounted(src, dst, limit)
	return err
}

func copyCounted(src, dst string, limit int64) (int64, error) {
	in, err := openRegular(src, limit)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- chemin construit ici
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("%s dépasse %d octets", filepath.Base(src), limit)
	}
	return n, err
}

// ArchiveTranscript copie tel quel le transcript rendu par l'agent dans le
// dossier d'archive, monté par les seules étapes de confiance, et rend son
// chemin. Le nom de l'archive vient de la tâche, écrite par une étape de
// confiance : passe, ticket et tour, jamais du contenu de l'agent. Une
// archive existante n'est pas écrasée. Sans transcript, il rend "" ; err
// signale un transcript annoncé mais refusé, ou une archive impossible.
func (d Dirs) ArchiveTranscript(res harness.Result, task harness.Task, archive string) (string, error) {
	if res.Transcript == "" {
		if res.SessionID != "" {
			return "", fmt.Errorf("transcript de la session %s absent de la sortie de l'agent (introuvable ou au-delà de %d octets)", res.SessionID, harness.MaxTranscriptBytes)
		}
		return "", nil
	}
	src, err := openRegular(filepath.Join(d.OutDir(), harness.TranscriptFile), harness.MaxTranscriptBytes)
	if err != nil {
		return "", fmt.Errorf("transcript refusé : %w", err)
	}
	defer func() { _ = src.Close() }()
	dst := filepath.Join(archive, TranscriptPath(task.PassID, task.Ticket.Repo, task.Ticket.Number, task.Round))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- nom construit ici depuis la tâche
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(src, harness.MaxTranscriptBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > harness.MaxTranscriptBytes {
		err = fmt.Errorf("transcript au-delà de %d octets", harness.MaxTranscriptBytes)
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// TranscriptPath est le chemin, relatif à l'archive, du transcript d'un tour
// sur un ticket : <passe>/<dépôt>-<numéro>-round<tour>.jsonl.
func TranscriptPath(pass, repo string, number, round int) string {
	return filepath.Join(safeName(pass), fmt.Sprintf("%s-%d-round%d.jsonl", safeName(repo), number, max(round, 1)))
}

// safeName rend s utilisable comme un seul composant de chemin.
func safeName(s string) string {
	s = unsafeChars.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
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
	f, err := openRegular(path, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// openRegular ouvre un fichier ordinaire, sans suivre de lien symbolique,
// d'au plus limit octets à l'ouverture ; la lecture reste à borner.
func openRegular(path string, limit int64) (*os.File, error) {
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
	return f, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
