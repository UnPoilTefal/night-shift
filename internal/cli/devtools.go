package cli

import (
	"context"
	"flag"
	"io"
	"os"
)

// Ce fichier expose aux outils de développement (ADR 0014) la lecture des
// options de forge et des secrets, pour qu'ils suivent les règles des étapes
// sans que le binaire publié dépende d'eux.

// ForgeFlags porte le choix de la forge et de ses adresses.
type ForgeFlags struct{ flags *forgeFlags }

// AddForgeFlags déclare les options de forge sur fs.
func AddForgeFlags(fs *flag.FlagSet, withGit bool) ForgeFlags {
	return ForgeFlags{addForgeFlags(fs, withGit)}
}

// Open construit l'adaptateur choisi, comme le fait une étape : une erreur
// d'usage est signalée sur stderr, avant tout appel réseau.
func (ff ForgeFlags) Open(cmd string, stderr io.Writer, repos ...string) (Forge, string, bool) {
	return ff.flags.open(cmd, stderr, repos...)
}

// Name rend le nom de la forge choisie.
func (ff ForgeFlags) Name() string { return ff.flags.name }

// Instance rend l'adresse de l'instance choisie, vide pour la forge publique.
func (ff ForgeFlags) Instance() string { return ff.flags.instance }

// UseTokenFile fait lire le jeton de la forge choisie dans ce fichier,
// quoi que porte l'environnement de l'appelant.
func (ff ForgeFlags) UseTokenFile(path string) error {
	a, err := lookupForge(ff.flags.name)
	if err != nil {
		return err
	}
	if err := os.Unsetenv(a.tokenEnv); err != nil {
		return err
	}
	return os.Setenv(a.tokenEnv+FileSuffix, path)
}

// SecretEnv rend le secret de la variable name, lu dans name ou dans le
// fichier que nomme name_FILE.
func SecretEnv(name string) (string, error) { return secretEnv(name) }

// SignalContext rend un contexte annulé par une interruption.
func SignalContext() (context.Context, context.CancelFunc) { return signalContext() }

// RepoList est une option répétable, ou une liste séparée par des virgules.
type RepoList = repoList
