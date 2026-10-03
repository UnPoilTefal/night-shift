package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/gitdiff"
	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/zones"
)

// AgentBranchPrefix est le préfixe des branches poussées par la Publication.
const AgentBranchPrefix = "agent/"

// runZones exécute le check des zones. Code de sortie : 0 pour pass et flag,
// 1 pour block ou pour une adhésion absente ou invalide, 2 pour un usage
// incorrect.
func runZones(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("zones", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", ".", "chemin du dépôt git")
	base := fs.String("base", "", "révision de la branche de base (obligatoire)")
	head := fs.String("head", "HEAD", "révision de la tête de la PR")
	headRef := fs.String("head-ref", "", "nom de la branche de la PR ; hors préfixe agent/, le check ne s'applique pas")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" {
		_, _ = fmt.Fprintln(stderr, "zones : --base est obligatoire")
		return 2
	}

	if *headRef != "" && !strings.HasPrefix(*headRef, AgentBranchPrefix) {
		_, _ = fmt.Fprintf(stdout, "verdict: pass\nPR hors agent (%s) : check des zones non applicable\n", *headRef)
		return 0
	}

	repo, err := gitdiff.Open(*repoDir, *base, *head)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "zones : %v\n", err)
		return 1
	}

	// L'adhésion est lue sur la base, jamais sur la PR : une PR ne peut pas
	// assouplir les règles qui la jugent.
	data, ok, err := repo.File(*base, optin.Path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "zones : lecture de %s : %v\n", optin.Path, err)
		return 1
	}
	if !ok {
		_, _ = fmt.Fprintf(stderr, "zones : pas d'adhésion (%s) sur la base %s : le dépôt n'a pas adhéré\n", optin.Path, *base)
		return 1
	}
	o, err := optin.Parse(data)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "zones : %s sur la base %s : %v\n", optin.Path, *base, err)
		return 1
	}

	changes, err := repo.Changes()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "zones : %v\n", err)
		return 1
	}
	report, err := zones.Check(o, changes, repo)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "zones : %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "verdict: %s\n", report.Verdict)
	for _, f := range report.Findings {
		_, _ = fmt.Fprintf(stdout, "%s %s (%s, motif %s)\n", f.Verdict, f.Path, f.Reason, f.Zone)
	}
	if report.Verdict == zones.Block {
		return 1
	}
	return 0
}
