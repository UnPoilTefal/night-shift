// Package publication est l'étape de confiance, sans modèle, qui transforme
// la série de commits d'un agent en branche agent/ et en PR en brouillon
// (ADR 0005). Elle repart d'un clone neuf : rien de ce que l'agent a pu
// laisser dans son propre clone (hooks, configuration) n'est exécuté ici.
package publication

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/harness"
)

// Git publie par git et ouvre la PR par l'adaptateur de forge.
type Git struct {
	Forge forge.Forge
	// GitURL est la base des adresses de clone (https://github.com).
	GitURL string
	Token  string
}

// Publish implémente pass.Publisher.
func (g Git) Publish(ctx context.Context, task harness.Task, res harness.Result) (string, error) {
	t := task.Ticket
	remote := gitrepo.Remote{BaseURL: g.GitURL, Token: g.Token}
	tmp, err := os.MkdirTemp("", "night-shift-publish-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dir := filepath.Join(tmp, "repo")

	base, head, err := remote.Clone(ctx, t.Repo, dir)
	if err != nil {
		return "", err
	}
	if task.BaseBranch != "" {
		base = task.BaseBranch
	}
	if task.BaseSHA != "" {
		head = task.BaseSHA
	}
	branch := Branch(t)
	if _, err := remote.Git(ctx, dir, "checkout", "--quiet", "-b", branch, head); err != nil {
		return "", err
	}

	agent := res.Agent
	if agent == "" {
		agent = "unknown"
	}
	files := make([]string, len(res.Patches))
	for i, p := range res.Patches {
		files[i] = filepath.Join(tmp, fmt.Sprintf("%04d.patch", i+1))
		if err := os.WriteFile(files[i], p, 0o600); err != nil {
			return "", err
		}
	}
	trailers := []string{"interpret-trailers", "--in-place",
		"--trailer", "Night-Shift-Agent: " + oneLine(agent),
		"--trailer", "Night-Shift-Pass: " + oneLine(task.PassID)}
	if _, err := remote.Git(ctx, dir, append(trailers, files...)...); err != nil {
		return "", err
	}
	if _, err := remote.Git(ctx, dir, append([]string{"am", "--quiet", "--3way", "--keep-cr"}, files...)...); err != nil {
		return "", fmt.Errorf("la série de l'agent ne s'applique pas : %w", err)
	}

	ref := "refs/heads/" + branch
	if _, err := remote.Git(ctx, dir, "push", "--quiet", "--no-follow-tags", "origin", ref+":"+ref); err != nil {
		return "", err
	}
	// La passe efface le titre d'un ticket dont l'auteur n'est pas de
	// confiance : la PR nomme alors le ticket.
	title := t.Title
	if title == "" {
		title = fmt.Sprintf("night-shift : ticket #%d", t.Number)
	}
	return g.Forge.OpenDraftPR(ctx, t.Repo, forge.PullRequest{
		Head:  branch,
		Base:  base,
		Title: title,
		Body: fmt.Sprintf("Closes #%d\n\nPR ouverte en brouillon par night-shift (passe %s, agent %s). "+
			"Elle n'est jamais passée en prête ni mergée par night-shift : à relire par un humain.",
			t.Number, oneLine(task.PassID), oneLine(agent)),
	})
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// accents translittère les lettres accentuées courantes, pour qu'un titre
// en français donne un slug lisible.
var accents = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "á", "a", "ã", "a", "å", "a",
	"ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "í", "i", "ì", "i", "ñ", "n",
	"ô", "o", "ö", "o", "ó", "o", "ò", "o", "õ", "o", "ø", "o",
	"ù", "u", "û", "u", "ü", "u", "ú", "u", "ÿ", "y", "ý", "y",
	"œ", "oe", "æ", "ae", "ß", "ss",
)

// Branch rend la branche agent/<n°>-<slug> d'un ticket.
func Branch(t forge.Ticket) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(accents.Replace(strings.ToLower(t.Title)), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return fmt.Sprintf("agent/%d", t.Number)
	}
	return fmt.Sprintf("agent/%d-%s", t.Number, slug)
}

// oneLine empêche une valeur d'ajouter des lignes à un trailer ou à un corps.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
