// Command night-shift-proof rejoue l'épreuve des barrières. C'est un outil de
// développement (ADR 0014), lancé depuis un checkout par make proof-replay :
// ni lui ni les fiches de cas n'entrent dans les images publiées.
package main

import (
	"os"

	"github.com/UnPoilTefal/night-shift/internal/proofcli"
)

func main() {
	os.Exit(proofcli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
