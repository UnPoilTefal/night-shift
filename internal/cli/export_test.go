package cli

import (
	"slices"
	"testing"
)

// AddForge inscrit une forge au registre le temps d'un test : c'est ainsi
// qu'un nouvel adaptateur rejoint les étapes.
func AddForge(t testing.TB, name, tokenEnv string, open func(apiURL, gitURL, token string) Forge) {
	t.Helper()
	previous := forges
	forges = append(slices.Clone(forges), forgeAdapter{name: name, tokenEnv: tokenEnv, open: open})
	t.Cleanup(func() { forges = previous })
}
