package cli

import (
	"slices"
	"testing"
)

// AddForge inscrit au registre, le temps d'un test, une forge et ses
// adresses par défaut, comme le ferait une nouvelle entrée de forges.
func AddForge(t testing.TB, name, tokenEnv, apiURL, gitURL string, open func(apiURL, gitURL, token string) Forge) {
	t.Helper()
	previous := forges
	forges = append(slices.Clone(forges), forgeAdapter{
		name: name, tokenEnv: tokenEnv, apiURL: apiURL, gitURL: gitURL, open: open,
		repoForm: "propriétaire/nom", validRepo: func(string) error { return nil },
	})
	t.Cleanup(func() { forges = previous })
}

// ForgeSecretEnv rend les variables de jeton de toutes les forges du
// registre.
var ForgeSecretEnv = forgeSecretEnv
