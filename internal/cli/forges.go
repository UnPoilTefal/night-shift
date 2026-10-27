package cli

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/github"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab"
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
	// apiURL et gitURL sont les adresses de la forge publique, par défaut ;
	// instance déduit les deux d'une instance dédiée ou auto-hébergée.
	apiURL, gitURL string
	instance       func(base string) (apiURL, gitURL string)
	// tokenEnv est la variable qui porte le jeton de cette forge.
	tokenEnv string
	// toolEnv sont les autres variables où les outils de cette forge
	// cherchent un jeton, et tokenPrefixes les préfixes de ses jetons :
	// l'étape agent refuse les unes comme les autres.
	toolEnv       []string
	tokenPrefixes []string
	// repoForm décrit la forme d'un dépôt sur cette forge, et validRepo la
	// vérifie.
	repoForm  string
	validRepo func(repo string) error
	open      func(apiURL, gitURL, token string) Forge
}

// defaultForge est la forge sans --forge, et celle d'une tâche écrite par une
// version qui ne l'enregistrait pas.
const defaultForge = "github"

// TokenEnv est la variable d'environnement qui porte le jeton de la forge
// par défaut.
const TokenEnv = "NIGHT_SHIFT_GITHUB_TOKEN"

// GitLabTokenEnv est la variable d'environnement qui porte le jeton de la
// forge gitlab.
const GitLabTokenEnv = "NIGHT_SHIFT_GITLAB_TOKEN"

var forges = []forgeAdapter{{
	name:          defaultForge,
	apiURL:        github.DefaultBaseURL,
	gitURL:        github.DefaultGitURL,
	instance:      github.Instance,
	tokenEnv:      TokenEnv,
	toolEnv:       github.ToolTokenEnv,
	tokenPrefixes: github.TokenPrefixes,
	repoForm:      "propriétaire/nom",
	validRepo:     github.ValidRepo,
	open:          func(apiURL, gitURL, token string) Forge { return github.New(apiURL, gitURL, token) },
}, {
	name:          "gitlab",
	apiURL:        gitlab.DefaultBaseURL,
	gitURL:        gitlab.DefaultGitURL,
	instance:      gitlab.Instance,
	tokenEnv:      GitLabTokenEnv,
	toolEnv:       gitlab.ToolTokenEnv,
	tokenPrefixes: gitlab.TokenPrefixes,
	repoForm:      "groupe/projet, sous-groupes compris,",
	validRepo:     gitlab.ValidRepo,
	open:          func(apiURL, gitURL, token string) Forge { return gitlab.New(apiURL, gitURL, token) },
}}

func lookupForge(name string) (forgeAdapter, error) {
	for _, f := range forges {
		if f.name == name {
			return f, nil
		}
	}
	return forgeAdapter{}, fmt.Errorf("forge %q inconnue, forges disponibles : %s", name, forgeNames())
}

// forgeFlags porte le choix de la forge et de ses adresses ; une adresse
// vide prend la valeur de l'adaptateur choisi.
type forgeFlags struct {
	name, instance, apiURL, gitURL string
	// withGit dit si l'étape clone ou pousse, et prend donc --git-url.
	withGit bool
}

func addForgeFlags(fs *flag.FlagSet, withGit bool) *forgeFlags {
	ff := &forgeFlags{withGit: withGit}
	fs.StringVar(&ff.name, "forge", defaultForge, "adaptateur de forge : "+forgeNames())
	fs.StringVar(&ff.instance, "instance", "", "adresse de base d'une instance dédiée ou auto-hébergée, dont l'adaptateur déduit l'API et les clones (par défaut, la forge publique : "+
		forgeDefaults(func(f forgeAdapter) string { return f.gitURL })+")")
	fs.StringVar(&ff.apiURL, "api-url", "", "URL de l'API, pour une topologie que --instance ne décrit pas")
	if withGit {
		fs.StringVar(&ff.gitURL, "git-url", "", "base des adresses de clone, à donner avec --api-url")
	}
	return ff
}

// addresses rend les adresses de l'API et des clones : celles de la forge
// publique, celles que l'adaptateur déduit de --instance, ou la paire
// --api-url et --git-url, toujours donnée entière. Deux adresses ne peuvent
// ainsi désigner deux instances : le jeton de l'une partirait vers l'autre.
func (ff *forgeFlags) addresses(a forgeAdapter) (apiURL, gitURL string, err error) {
	pair := ff.apiURL != "" || ff.gitURL != ""
	switch {
	case ff.instance != "" && pair:
		return "", "", fmt.Errorf("--instance exclut --api-url et --git-url")
	case ff.instance != "":
		u, err := url.Parse(ff.instance)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			// L'adresse n'est pas recopiée : elle peut porter un identifiant.
			return "", "", fmt.Errorf("--instance invalide, attendu une adresse http(s) sans identifiant, requête ni fragment")
		}
		apiURL, gitURL = a.instance(ff.instance)
		return apiURL, gitURL, nil
	case ff.withGit && (ff.apiURL == "") != (ff.gitURL == ""):
		return "", "", fmt.Errorf("--api-url et --git-url désignent la même instance : les donner ensemble, ou employer --instance")
	case ff.apiURL != "":
		return ff.apiURL, ff.gitURL, nil
	}
	return a.apiURL, a.gitURL, nil
}

// repoUsage décrit --repo, dont la forme dépend de la forge.
func repoUsage() string {
	return "dépôt candidat : " + forgeDefaults(func(f forgeAdapter) string { return f.repoForm }) + " (répétable, ou liste séparée par des virgules)"
}

// open construit l'adaptateur choisi avec le jeton de sa variable, après
// avoir vérifié la forme des dépôts candidats et les adresses. Une forge
// inconnue, un dépôt mal formé, des adresses incohérentes ou un jeton absent
// est une erreur d'usage, signalée avant tout appel réseau.
func (ff *forgeFlags) open(cmd string, stderr io.Writer, repos ...string) (Forge, string, bool) {
	a, err := lookupForge(ff.name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, err)
		return nil, "", false
	}
	for _, r := range repos {
		if err := a.validRepo(r); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s : forge %s : %v\n", cmd, a.name, err)
			return nil, "", false
		}
	}
	apiURL, gitURL, err := ff.addresses(a)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, err)
		return nil, "", false
	}
	token, err := secretEnv(a.tokenEnv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, err)
		return nil, "", false
	}
	if token == "" {
		_, _ = fmt.Fprintf(stderr, "%s : la variable %s, ou %s, doit porter le jeton de la forge %s\n", cmd, a.tokenEnv, a.tokenEnv+FileSuffix, a.name)
		return nil, "", false
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

// forgeDefaults décrit, forge par forge, une valeur par défaut de son
// adaptateur.
func forgeDefaults(value func(forgeAdapter) string) string {
	out := make([]string, 0, len(forges))
	for _, f := range forges {
		out = append(out, value(f)+" pour "+f.name)
	}
	return strings.Join(out, ", ")
}

// ForgeSecretEnv rend les variables de jeton de toutes les forges du
// registre : aucune n'atteint l'agent, et un déploiement n'en monte aucune
// dans une étape agent.
func ForgeSecretEnv() []string {
	var out []string
	for _, f := range forges {
		out = append(append(out, f.tokenEnv), f.toolEnv...)
	}
	return out
}

// forgeToken rend le nom de la première variable de env (au format
// NOM=valeur) qui porte un jeton de forge, par son nom ou par la forme de sa
// valeur, quelle que soit la forge du registre. Ce n'est qu'un garde-fou :
// un jeton monté dans un fichier lui échappe (passSecretFile ne voit que la
// variable qui le nomme), et la garantie reste le déploiement (ADR 0011).
func forgeToken(env []string) (name string, ok bool) {
	names := ForgeSecretEnv()
	var prefixes []string
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
