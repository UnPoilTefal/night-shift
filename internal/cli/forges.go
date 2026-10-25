package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/github"
)

// Forge est ce qu'un adaptateur de forge fournit aux étapes : la source de
// tickets et la forge de code (ADR 0012).
type Forge interface {
	forge.TicketSource
	forge.CodeForge
}

// forgeAdapter est une entrée du registre des forges : ajouter une forge,
// c'est ajouter une entrée, sans toucher aux étapes ni à la passe.
type forgeAdapter struct {
	name string
	// apiURL et gitURL sont les valeurs par défaut de --api-url et --git-url.
	apiURL, gitURL string
	// tokenEnv est la variable qui porte le jeton de cette forge.
	tokenEnv string
	// toolEnv sont les autres variables où les outils de cette forge
	// cherchent un jeton, et tokenPrefixes les préfixes de ses jetons :
	// l'étape agent refuse les unes comme les autres.
	toolEnv       []string
	tokenPrefixes []string
	open          func(apiURL, gitURL, token string) Forge
}

// defaultForge est la forge sans --forge, et celle d'une tâche écrite par une
// version qui ne l'enregistrait pas.
const defaultForge = "github"

// TokenEnv est la variable d'environnement qui porte le jeton de la forge
// par défaut.
const TokenEnv = "NIGHT_SHIFT_GITHUB_TOKEN"

var forges = []forgeAdapter{{
	name:          defaultForge,
	apiURL:        github.DefaultBaseURL,
	gitURL:        github.DefaultGitURL,
	tokenEnv:      TokenEnv,
	toolEnv:       []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"},
	tokenPrefixes: []string{"ghp_", "github_pat_", "gho_", "ghs_", "ghu_"},
	open:          func(apiURL, gitURL, token string) Forge { return github.New(apiURL, gitURL, token) },
}}

// otherTokenPrefixes sont les préfixes de jetons de forges qui n'ont pas
// encore d'adaptateur : l'étape agent les refuse aussi.
var otherTokenPrefixes = []string{"glpat-"}

func lookupForge(name string) (forgeAdapter, error) {
	names := make([]string, 0, len(forges))
	for _, f := range forges {
		if f.name == name {
			return f, nil
		}
		names = append(names, f.name)
	}
	return forgeAdapter{}, fmt.Errorf("forge %q inconnue, forges disponibles : %s", name, strings.Join(names, ", "))
}

// forgeFlags porte le choix de la forge et de ses adresses ; une adresse
// vide prend la valeur de l'adaptateur choisi.
type forgeFlags struct {
	name, apiURL, gitURL string
}

func addForgeFlags(fs *flag.FlagSet, withGit bool) *forgeFlags {
	ff := &forgeFlags{}
	fs.StringVar(&ff.name, "forge", defaultForge, "adaptateur de forge : "+forgeNames())
	fs.StringVar(&ff.apiURL, "api-url", "", "URL de l'API de la forge (par défaut celle de l'adaptateur)")
	if withGit {
		fs.StringVar(&ff.gitURL, "git-url", "", "base des adresses de clone (par défaut celle de l'adaptateur)")
	}
	return ff
}

// open construit l'adaptateur choisi avec le jeton de sa variable. Une forge
// inconnue ou un jeton absent est une erreur d'usage, signalée avant tout
// appel réseau.
func (ff *forgeFlags) open(cmd string, stderr io.Writer) (Forge, string, bool) {
	a, err := lookupForge(ff.name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, err)
		return nil, "", false
	}
	token := os.Getenv(a.tokenEnv)
	if token == "" {
		_, _ = fmt.Fprintf(stderr, "%s : la variable %s doit porter le jeton de la forge %s\n", cmd, a.tokenEnv, a.name)
		return nil, "", false
	}
	apiURL, gitURL := ff.apiURL, ff.gitURL
	if apiURL == "" {
		apiURL = a.apiURL
	}
	if gitURL == "" {
		gitURL = a.gitURL
	}
	return a.open(apiURL, gitURL, token), token, true
}

func forgeNames() string {
	names := make([]string, 0, len(forges))
	for _, f := range forges {
		names = append(names, f.name)
	}
	return strings.Join(names, ", ")
}

// forgeSecretEnv rend les variables de jeton de toutes les forges du
// registre : aucune n'atteint l'agent.
func forgeSecretEnv() []string {
	var out []string
	for _, f := range forges {
		out = append(append(out, f.tokenEnv), f.toolEnv...)
	}
	return out
}

// forgeToken rend le nom de la première variable de env (au format
// NOM=valeur) qui porte un jeton de forge, par son nom ou par la forme de sa
// valeur, quelle que soit la forge du registre. Ce n'est qu'un garde-fou :
// un jeton monté dans un fichier lui échappe, et la garantie reste le
// déploiement (ADR 0011).
func forgeToken(env []string) (name string, ok bool) {
	names := forgeSecretEnv()
	prefixes := slices.Clone(otherTokenPrefixes)
	for _, f := range forges {
		prefixes = append(prefixes, f.tokenPrefixes...)
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if slices.Contains(names, k) || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(v, p) }) {
			return k, true
		}
	}
	return "", false
}
