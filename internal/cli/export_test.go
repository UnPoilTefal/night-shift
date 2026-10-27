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
		instance: func(base string) (string, string) { return base + "/api", base },
		repoForm: "propriétaire/nom", validRepo: func(string) error { return nil },
	})
	t.Cleanup(func() { forges = previous })
}

// SecretEnv expose la lecture d'un secret, par sa variable ou son fichier.
var SecretEnv = secretEnv
