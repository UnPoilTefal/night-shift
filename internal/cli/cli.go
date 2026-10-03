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
	default:
		_, _ = fmt.Fprintf(stderr, "commande inconnue : %q\n\n%s", args[0], usage)
		return 2
	}
}
