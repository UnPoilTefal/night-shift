// Package gitrepo exécute git pour les étapes de confiance (sélection et
// Publication) : clone du dépôt cible et push d'une branche. Le jeton de
// forge passe par l'environnement de git, jamais par l'URL ni par la
// configuration du clone, si bien qu'un clone confié à l'agent n'en garde
// aucune trace.
package gitrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Remote désigne une forge git : <BaseURL>/<propriétaire>/<nom>.git.
type Remote struct {
	// BaseURL est https://github.com, ou un dossier pour les tests.
	BaseURL string
	// Token, s'il est renseigné, est présenté en en-tête HTTP.
	Token string
	// Name et Email signent les commits que git crée lui-même.
	Name, Email string
}

// URL rend l'adresse de clone d'un dépôt.
func (r Remote) URL(repo string) string {
	return strings.TrimSuffix(r.BaseURL, "/") + "/" + repo + ".git"
}

// Clone clone repo dans dir et rend sa branche par défaut et son commit de
// tête.
func (r Remote) Clone(ctx context.Context, repo, dir string) (branch, sha string, err error) {
	if _, err := r.Git(ctx, "", "clone", "--quiet", "--no-tags", r.URL(repo), dir); err != nil {
		return "", "", err
	}
	if branch, err = r.Git(ctx, dir, "symbolic-ref", "--short", "HEAD"); err != nil {
		return "", "", err
	}
	if sha, err = r.Git(ctx, dir, "rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	return branch, sha, nil
}

// Git exécute git dans dir, sans configuration système ni globale, et rend
// sa sortie standard. Le jeton n'apparaît jamais dans une erreur.
func (r Remote) Git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- arguments fixés par l'appelant, sans shell
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+r.name(), "GIT_AUTHOR_EMAIL="+r.email(),
		"GIT_COMMITTER_NAME="+r.name(), "GIT_COMMITTER_EMAIL="+r.email(),
	)
	if r.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.Token))
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
		)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s : %w : %s", args[0], err, r.redact(strings.TrimSpace(stderr.String())))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (r Remote) name() string {
	if r.Name != "" {
		return r.Name
	}
	return "night-shift"
}

func (r Remote) email() string {
	if r.Email != "" {
		return r.Email
	}
	return "night-shift@users.noreply.github.com"
}

func (r Remote) redact(s string) string {
	if r.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, r.Token, "[jeton masqué]")
}
