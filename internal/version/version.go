// Package version porte la version du binaire, injectée au build par
// -ldflags "-X github.com/UnPoilTefal/night-shift/internal/version.Version=<version>".
package version

// Version vaut "dev" pour un binaire construit sans injection.
var Version = "dev"
