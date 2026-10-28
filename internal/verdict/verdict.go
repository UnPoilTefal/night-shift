// Package verdict tient le journal des verdicts (voir GLOSSARY.md, terme
// Verdict) : la décision qu'une réalisation rend sur une action de l'agent,
// dans la forme fixée par keystone#78. Le journal vit sur la plateforme, à
// côté des transcripts, hors de portée de l'agent. Il est écrit par les
// étapes de confiance qui observent la décision, jamais par l'agent. Il ne
// porte jamais le verbatim d'une action, qui peut contenir un secret et
// reste dans le transcript, et jamais une décision humaine, dont le support
// est le ticket ou la configuration de l'agent.
package verdict

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Result est le résultat d'un verdict, dans le vocabulaire de l'épreuve.
type Result string

// Résultats d'un verdict.
const (
	// Refused : l'action est refusée (bloquée).
	Refused Result = "refused"
	// Gated : l'action est renvoyée à la porte, une demande d'accord est
	// émise.
	Gated Result = "gated"
	// Expired : la demande d'accord a expiré sans réponse.
	Expired Result = "expired"
	// Flagged : l'action a eu lieu, elle est signalée (détectée).
	Flagged Result = "flagged"
	// Left : l'action est laissée ; on ne l'enregistre que pour le
	// classifieur sur une action « à demander ».
	Left Result = "left"
	// Stopped : un plafond a arrêté l'exécution.
	Stopped Result = "stopped"
)

var order = []Result{Refused, Gated, Expired, Flagged, Stopped, Left}

// Label rend le résultat dans la langue des rapports, au pluriel si n > 1.
func (r Result) Label(n int) string {
	l := map[Result]string{Refused: "refusée", Gated: "renvoyée à la porte", Expired: "refusée à expiration",
		Flagged: "signalée", Left: "laissée", Stopped: "arrêtée"}[r]
	if l == "" {
		l = string(r)
	}
	if n > 1 {
		if i := strings.Index(l, " "); i > 0 {
			return l[:i] + "s" + l[i:]
		}
		return l + "s"
	}
	return l
}

// Verdict est une entrée du journal.
type Verdict struct {
	// Identification.
	At       time.Time `json:"at"`
	Platform string    `json:"platform"`
	Pass     string    `json:"pass"`
	Ticket   string    `json:"ticket,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	// AgentConfig est la révision de la configuration de l'agent,
	// dépôt@révision, vide sans configuration.
	AgentConfig string `json:"agentConfig,omitempty"`
	// Rendu : la brique du workflow cible, la réalisation et sa règle, le
	// résultat, et l'effet (faux en mode ombre).
	Brick       string `json:"brick"`
	Realization string `json:"realization"`
	Rule        string `json:"rule,omitempty"`
	Result      Result `json:"result"`
	Effect      bool   `json:"effect"`
	// Action : sa classe et son empreinte, jamais son verbatim.
	ActionClass string `json:"actionClass"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// WrittenBy est l'étape ou la brique qui a observé la décision.
	WrittenBy string `json:"writtenBy"`
}

// Platform nomme la plateforme dans les verdicts que night-shift écrit.
const Platform = "night-shift"

// File est le nom du journal d'une passe dans l'archive.
const File = "verdicts.jsonl"

// Fingerprint rend l'empreinte normalisée d'une action : espaces réduits,
// sha256 tronqué. Elle permet de reconnaître une action répétée sans la
// garder.
func Fingerprint(action string) string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(action), " ")))
	return hex.EncodeToString(h[:8])
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// path rend le journal de la passe dans l'archive.
func path(archive, pass string) string {
	name := unsafeChars.ReplaceAllString(pass, "_")
	if name == "" || name == "." || name == ".." {
		name = "_"
	}
	return filepath.Join(archive, name, File)
}

// Append ajoute des verdicts au journal de leur passe dans l'archive. Le
// journal ne s'écrit qu'en ajout ; un lien symbolique à sa place est
// refusé.
func Append(archive string, vs ...Verdict) error {
	byPass := map[string][]Verdict{}
	for _, v := range vs {
		if v.Pass == "" {
			return errors.New("verdict sans passe")
		}
		byPass[v.Pass] = append(byPass[v.Pass], v)
	}
	for pass, vs := range byPass {
		p := path(archive, pass)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- nom construit ici depuis la passe
		if err != nil {
			return err
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		for _, v := range vs {
			if err := enc.Encode(v); err != nil { // #nosec G117 -- Pass est l'identifiant de la passe, pas un secret
				_ = f.Close()
				return err
			}
		}
		_, err = f.Write(b.Bytes())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Read relit le journal d'une passe ; un journal absent est vide.
func Read(archive, pass string) ([]Verdict, error) {
	f, err := os.OpenFile(path(archive, pass), os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- idem
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var vs []Verdict
	dec := json.NewDecoder(f)
	for {
		var v Verdict
		if err := dec.Decode(&v); errors.Is(err, io.EOF) {
			return vs, nil
		} else if err != nil {
			return vs, fmt.Errorf("journal des verdicts illisible : %w", err)
		}
		vs = append(vs, v)
	}
}

// Counts compte les verdicts par résultat.
func Counts(vs []Verdict) map[Result]int {
	c := map[Result]int{}
	for _, v := range vs {
		c[v.Result]++
	}
	return c
}

// Summary rend des compteurs en une ligne : « 2 refusées, 1 signalée » ;
// "" s'il n'y en a aucun.
func Summary(c map[Result]int) string {
	var parts []string
	for _, r := range order {
		if n := c[r]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, r.Label(n)))
		}
	}
	for r, n := range c {
		if !slices.Contains(order, r) && n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, r))
		}
	}
	return strings.Join(parts, ", ")
}

// Refusals relit un transcript de Claude Code (JSONL) et rend un verdict
// refusé par appel d'outil que le harness a refusé faute de permission.
// La classe d'action est l'outil, suivi pour Bash du premier mot de la
// commande ; le verbatim n'est jamais repris.
func Refusals(transcript []byte) []Verdict {
	type block struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	type line struct {
		Message struct {
			Content []block `json:"content"`
		} `json:"message"`
	}
	calls := map[string]block{}
	var out []Verdict
	sc := bufio.NewScanner(bytes.NewReader(transcript))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		for _, b := range l.Message.Content {
			switch b.Type {
			case "tool_use":
				calls[b.ID] = b
			case "tool_result":
				call, ok := calls[b.ToolUseID]
				if !ok || !b.IsError || !strings.Contains(strings.ToLower(string(b.Content)), "permission") {
					continue
				}
				delete(calls, b.ToolUseID)
				out = append(out, Verdict{
					Brick: "B28", Realization: "règles de refus du harness (R28.2)", Result: Refused, Effect: true,
					ActionClass: actionClass(call.Name, call.Input), Fingerprint: Fingerprint(string(call.Input)),
				})
			}
		}
	}
	return out
}

// actionClass rend la classe d'un appel d'outil : son nom, et pour Bash le
// premier mot de la commande (Bash(git), Bash(curl)…).
func actionClass(tool string, input json.RawMessage) string {
	if tool != "Bash" {
		return tool
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return tool
	}
	if f := strings.Fields(in.Command); len(f) > 0 {
		return "Bash(" + filepath.Base(f[0]) + ")"
	}
	return tool
}
