// Command night-shift lance les passes d'agents IA sur les tickets prêts.
package main

import (
	"os"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
