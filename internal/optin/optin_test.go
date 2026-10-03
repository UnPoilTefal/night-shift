package optin_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/optin"
)

const valid = `
version: 1
trustLevel:
  ticketsPerPass: 1
  pullRequests: draft
toolImage: ghcr.io/example/tools-go:1
trustedAuthors: [alice]
forbiddenZones:
  - ".github/**"
sensitiveZones:
  - path: go.mod
    kind: go-dependencies
  - path: "deploy/**"
`

func TestParseValid(t *testing.T) {
	o, err := optin.Parse([]byte(valid))
	if err != nil {
		t.Fatalf("erreur inattendue : %v", err)
	}
	if o.TrustLevel.TicketsPerPass != 1 || o.TrustLevel.PullRequests != optin.Draft {
		t.Fatalf("palier de confiance mal lu : %+v", o.TrustLevel)
	}
	if got := o.SensitiveZones[1].Kind; got != optin.Generic {
		t.Fatalf("kind par défaut = %q, attendu %q", got, optin.Generic)
	}
}

func TestForbiddenAlwaysIncludesImplicitZone(t *testing.T) {
	o, err := optin.Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Forbidden(), optin.ImplicitForbiddenZone) {
		t.Fatalf("zones interdites = %v, la zone implicite manque", o.Forbidden())
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"version inconnue":   {strings.Replace(valid, "version: 1", "version: 2", 1), "version 2"},
		"plafond nul":        {strings.Replace(valid, "ticketsPerPass: 1", "ticketsPerPass: 0", 1), "ticketsPerPass"},
		"mode de PR inconnu": {strings.Replace(valid, "pullRequests: draft", "pullRequests: merged", 1), "pullRequests"},
		"champ inconnu":      {valid + "autoMerge: true\n", "autoMerge"},
		"motif invalide":     {strings.Replace(valid, `".github/**"`, `"[.github"`, 1), "motif invalide"},
		"motif absolu":       {strings.Replace(valid, `".github/**"`, `"/etc/**"`, 1), "motif absolu"},
		"motif en ./":        {strings.Replace(valid, `".github/**"`, `"./.github/**"`, 1), "segment"},
		"motif avec ..":      {strings.Replace(valid, `".github/**"`, `"docs/../.github/**"`, 1), "segment"},
		"kind inconnu":       {strings.Replace(valid, "kind: go-dependencies", "kind: npm", 1), "kind"},
		"document vide":      {"", "illisible"},
		"yaml cassé":         {"version: [", "illisible"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := optin.Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("erreur attendue, adhésion acceptée")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erreur = %q, attendu une mention de %q", err, c.want)
			}
		})
	}
}
