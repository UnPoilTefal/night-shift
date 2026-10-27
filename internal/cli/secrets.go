package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// FileSuffix forme la variable qui donne un secret par un fichier : le jeton
// de NIGHT_SHIFT_GITHUB_TOKEN se lit aussi dans le fichier que nomme
// NIGHT_SHIFT_GITHUB_TOKEN_FILE. Sous Compose, un secret monté en fichier
// n'apparaît ni dans docker inspect ni dans l'environnement des processus
// enfants.
const FileSuffix = "_FILE"

// secretEnv rend le secret de la variable name, lu dans name ou dans le
// fichier que nomme name_FILE, débarrassé de son saut de ligne final ; un
// fichier vide vaut une absence. Les deux formes ensemble, ou un fichier
// illisible, sont une erreur d'usage, qui nomme les variables sans jamais
// citer de valeur.
func secretEnv(name string) (string, error) {
	v, path := os.Getenv(name), os.Getenv(name+FileSuffix)
	switch {
	case path == "":
		return v, nil
	case v != "":
		return "", fmt.Errorf("%s et %s sont exclusives : n'en donner qu'une", name, name+FileSuffix)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- chemin fourni par le déploiement
	if err != nil {
		return "", fmt.Errorf("le fichier de %s est illisible", name+FileSuffix)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}

// passSecretFile rend le nom de la première variable de env (au format
// NOM=valeur) qui donne par un fichier un secret des étapes de confiance :
// jeton de forge ou webhook. L'étape agent ne doit en voir aucune, même vers
// un fichier absent.
func passSecretFile(env []string) (name string, ok bool) {
	var names []string
	for _, n := range append(ForgeSecretEnv(), WebhookEnv) {
		names = append(names, n+FileSuffix)
	}
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); v != "" && slices.Contains(names, k) {
			return k, true
		}
	}
	return "", false
}

// withSecretFiles rend env sans aucune variable …_FILE, où chaque variable
// de names porte le secret lu dans sa forme fichier, s'il y en a un.
func withSecretFiles(env []string, names ...string) ([]string, error) {
	values := map[string]string{}
	for _, n := range names {
		v, err := secretEnv(n)
		if err != nil {
			return nil, err
		}
		values[n] = v
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := values[k]; !ok && !strings.HasSuffix(k, FileSuffix) {
			out = append(out, kv)
		}
	}
	for _, n := range names {
		if values[n] != "" {
			out = append(out, n+"="+values[n])
		}
	}
	return out, nil
}
