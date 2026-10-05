// Package cli aiguille les sous-commandes du binaire night-shift.
package cli

import (
	"fmt"
	"io"

	"github.com/UnPoilTefal/night-shift/internal/version"
)

const usage = `usage : night-shift <commande>

Commandes :
  version   affiche la version du binaire
  pass      lance une passe sur les tickets prêts des dépôts candidats, sans agent
  select    étape 1 : réserve un ticket et prépare le clone de travail (jeton de forge)
  agent     étape 2 : fait travailler claude -p sur le clone (aucun jeton de forge)
  publish   étape 3 : publie la série de l'agent en PR brouillon, suit la CI, prépare une relance ou rend le ticket (jeton de forge)
  zones     check des zones sur le diff d'une PR d'agent
`

// Run exécute la commande désignée par args (sans le nom du binaire) et rend
// le code de sortie du processus.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	case "pass":
		return runPass(args[1:], stdout, stderr)
	case "select":
		return runSelect(args[1:], stdout, stderr)
	case "agent":
		return runAgent(args[1:], stdout, stderr)
	case "publish":
		return runPublish(args[1:], stdout, stderr)
	case "zones":
		return runZones(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "commande inconnue : %q\n\n%s", args[0], usage)
		return 2
	}
}
