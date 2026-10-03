// Package zones rend le verdict du check des zones sur le diff d'une PR
// d'agent : bloquer si une zone interdite est touchée, signaler si une zone
// sensible l'est, passer sinon (voir ADR 0002).
package zones

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/mod/modfile"

	"github.com/UnPoilTefal/night-shift/internal/optin"
)

// Verdict est l'issue du check, ordonnée de la plus permissive à la plus
// stricte.
type Verdict int

// Verdicts possibles.
const (
	Pass Verdict = iota
	Flag
	Block
)

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "pass"
	case Flag:
		return "flag"
	case Block:
		return "block"
	default:
		return fmt.Sprintf("Verdict(%d)", int(v))
	}
}

// Change est un fichier modifié par la PR. OldPath n'est renseigné que pour
// un renommage.
type Change struct {
	Path    string
	OldPath string
}

// Contents donne accès au contenu d'un fichier avant (base) et après (head)
// la PR. ok vaut false si le fichier n'existe pas de ce côté.
type Contents interface {
	Base(path string) (data []byte, ok bool, err error)
	Head(path string) (data []byte, ok bool, err error)
}

// Finding est un chemin qui contribue au verdict.
type Finding struct {
	Verdict Verdict
	Path    string
	Zone    string
	Reason  string
}

// Report est le résultat du check.
type Report struct {
	Verdict  Verdict
	Findings []Finding
}

// Check confronte les changements d'une PR aux zones de l'adhésion.
func Check(o optin.OptIn, changes []Change, contents Contents) (Report, error) {
	var r Report
	for _, c := range changes {
		f, err := checkChange(o, c, contents)
		if err != nil {
			return Report{}, err
		}
		if f.Verdict == Pass {
			continue
		}
		r.Findings = append(r.Findings, f)
		r.Verdict = max(r.Verdict, f.Verdict)
	}
	return r, nil
}

func checkChange(o optin.OptIn, c Change, contents Contents) (Finding, error) {
	touched := []string{c.Path}
	if c.OldPath != "" && c.OldPath != c.Path {
		touched = append(touched, c.OldPath)
	}

	for _, p := range touched {
		if zone, ok := firstMatch(o.Forbidden(), p); ok {
			return Finding{Verdict: Block, Path: p, Zone: zone, Reason: "zone interdite"}, nil
		}
	}

	for _, z := range o.SensitiveZones {
		for _, p := range touched {
			if !doublestar.MatchUnvalidated(z.Path, p) {
				continue
			}
			switch z.Kind {
			case optin.GoDependencies:
				reason, err := goDependencyReason(p, contents)
				if err != nil {
					return Finding{}, err
				}
				if reason == "" {
					continue
				}
				return Finding{Verdict: Flag, Path: p, Zone: z.Path, Reason: reason}, nil
			default:
				return Finding{Verdict: Flag, Path: p, Zone: z.Path, Reason: "zone sensible"}, nil
			}
		}
	}
	return Finding{Verdict: Pass, Path: c.Path}, nil
}

func firstMatch(patterns []string, p string) (string, bool) {
	for _, pat := range patterns {
		if doublestar.MatchUnvalidated(pat, p) {
			return pat, true
		}
	}
	return "", false
}

// goDependencyReason rend un motif de signalement si un go.mod gagne une
// dépendance directe ou une directive replace ; une montée de version ou un
// retrait ne sont pas signalés. Un fichier autre qu'un go.mod n'est pas
// analysé.
func goDependencyReason(p string, contents Contents) (string, error) {
	if path.Base(p) != "go.mod" {
		return "", nil
	}
	head, ok, err := contents.Head(p)
	if err != nil {
		return "", err
	}
	if !ok {
		return "go.mod supprimé", nil
	}
	headMod, err := modfile.Parse(p, head, nil)
	if err != nil {
		return "", fmt.Errorf("go.mod illisible côté head (%s) : %w", p, err)
	}
	base, ok, err := contents.Base(p)
	if err != nil {
		return "", err
	}
	baseMod := &modfile.File{}
	if ok {
		if baseMod, err = modfile.Parse(p, base, nil); err != nil {
			return "", fmt.Errorf("go.mod illisible côté base (%s) : %w", p, err)
		}
	}

	direct := map[string]bool{}
	for _, r := range baseMod.Require {
		if !r.Indirect {
			direct[r.Mod.Path] = true
		}
	}
	var added []string
	for _, r := range headMod.Require {
		if !r.Indirect && !direct[r.Mod.Path] {
			added = append(added, r.Mod.Path)
		}
	}

	replaces := map[string]bool{}
	for _, r := range baseMod.Replace {
		replaces[r.Old.String()+"=>"+r.New.String()] = true
	}
	var replaced []string
	for _, r := range headMod.Replace {
		if !replaces[r.Old.String()+"=>"+r.New.String()] {
			replaced = append(replaced, r.Old.Path)
		}
	}

	var reasons []string
	if len(added) > 0 {
		slices.Sort(added)
		reasons = append(reasons, "nouvelle dépendance directe : "+strings.Join(added, ", "))
	}
	if len(replaced) > 0 {
		slices.Sort(replaced)
		reasons = append(reasons, "directive replace ajoutée ou modifiée : "+strings.Join(replaced, ", "))
	}
	return strings.Join(reasons, " ; "), nil
}
