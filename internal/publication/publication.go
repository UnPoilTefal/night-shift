// Package publication est l'étape de confiance, sans modèle, qui transforme
// la série de commits d'un agent en branche agent/ et en demande de
// changement en brouillon (ADR 0005). Elle repart d'un clone neuf : rien de ce que l'agent a pu
// laisser dans son propre clone (hooks, configuration) n'est exécuté ici.
package publication

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/gitdiff"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/zones"
)

// Git publie par git et ouvre la demande de changement par la forge de code
// du dépôt cible, qui décrit aussi l'accès git.
type Git struct {
	Code  forge.CodeForge
	Token string
}

// Publish implémente pass.Publisher. Au premier tour, elle crée la branche
// agent/ sur le commit de base et ouvre la demande de changement ; à une
// relance, elle ajoute la série par-dessus la tête de la demande et pousse
// sans forcer. Avant chaque
// push, elle juge chaque commit de la série contre l'adhésion du commit de
// base du premier tour (ADR 0011) : une zone interdite touchée, ou une
// adhésion absente ou invalide, et rien n'est poussé ; une zone sensible
// touchée est signalée sur la demande.
func (g Git) Publish(ctx context.Context, task harness.Task, res harness.Result) (pass.Published, error) {
	t := task.Ticket
	branch := Branch(t)
	if task.ChangeRequest != nil {
		branch = task.ChangeRequest.Branch
		if !strings.HasPrefix(branch, "agent/") {
			return pass.Published{}, fmt.Errorf("branche %q hors du préfixe agent/", branch)
		}
	}
	repo := task.TargetRepo
	remote := gitrepo.Remote{GitAccess: g.Code.GitAccess(repo), Token: g.Token}
	tmp, err := os.MkdirTemp("", "night-shift-publish-")
	if err != nil {
		return pass.Published{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dir := filepath.Join(tmp, "repo")

	base, head, err := remote.Clone(ctx, dir)
	if err != nil {
		return pass.Published{}, err
	}
	if task.BaseBranch != "" {
		base = task.BaseBranch
	}
	if task.BaseSHA != "" {
		head = task.BaseSHA
	}
	if _, err := remote.Git(ctx, dir, "checkout", "--quiet", "-b", branch, head); err != nil {
		return pass.Published{}, err
	}

	agent := res.Agent
	if agent == "" {
		agent = "unknown"
	}
	files := make([]string, len(res.Patches))
	for i, p := range res.Patches {
		files[i] = filepath.Join(tmp, fmt.Sprintf("%04d.patch", i+1))
		if err := os.WriteFile(files[i], p, 0o600); err != nil {
			return pass.Published{}, err
		}
	}
	trailers := []string{"interpret-trailers", "--in-place",
		"--trailer", "Night-Shift-Agent: " + oneLine(agent),
		"--trailer", "Night-Shift-Pass: " + oneLine(task.PassID)}
	if _, err := remote.Git(ctx, dir, append(trailers, files...)...); err != nil {
		return pass.Published{}, err
	}
	if _, err := remote.Git(ctx, dir, append([]string{"am", "--quiet", "--3way", "--keep-cr"}, files...)...); err != nil {
		return pass.Published{}, fmt.Errorf("la série de l'agent ne s'applique pas : %w", err)
	}
	pushed, err := remote.Git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return pass.Published{}, err
	}
	optInSHA := task.OptInSHA
	if optInSHA == "" && task.ChangeRequest == nil {
		optInSHA = head
	}
	flagged, err := check(ctx, remote, dir, optInSHA, head)
	if err != nil {
		return pass.Published{}, err
	}

	ref := "refs/heads/" + branch
	if _, err := remote.Git(ctx, dir, "push", "--quiet", "--no-follow-tags", "origin", ref+":"+ref); err != nil {
		return pass.Published{}, err
	}
	if task.ChangeRequest != nil {
		pub := pass.Published{Draft: *task.ChangeRequest, Head: pushed}
		if len(flagged) == 0 {
			return pub, nil
		}
		if err := g.Code.CommentChangeRequest(ctx, repo, pub.Draft.Number, fmt.Sprintf(
			"night-shift (passe %s) : le tour %d de l'agent touche une zone sensible, à un humain de décider.\n\n%s",
			oneLine(task.PassID), max(task.Round, 1), mention(flagged))); err != nil {
			return pub, err
		}
		return pub, g.Code.AddChangeRequestLabel(ctx, repo, pub.Draft.Number, forge.LabelSensitiveZone)
	}
	// La passe efface le titre d'un ticket dont l'auteur n'est pas de
	// confiance : la demande nomme alors le ticket.
	title := t.Title
	if title == "" {
		title = fmt.Sprintf("night-shift : ticket #%d", t.Number)
	}
	var labels []string
	if len(flagged) > 0 {
		labels = []string{forge.LabelSensitiveZone}
	}
	number, url, err := g.Code.OpenChangeRequest(ctx, repo, forge.ChangeRequest{
		Head:   branch,
		Base:   base,
		Title:  title,
		Labels: labels,
		Body: fmt.Sprintf("Closes #%d\n\nPR ouverte en brouillon par night-shift (passe %s, agent %s). "+
			"Elle n'est jamais passée en prête ni mergée par night-shift : à relire par un humain.%s",
			t.Number, oneLine(task.PassID), oneLine(agent), sensitiveNote(flagged)),
	})
	if url == "" {
		return pass.Published{}, err
	}
	return pass.Published{Draft: forge.Draft{Number: number, URL: url, Branch: branch}, Head: pushed}, err
}

// check juge chaque commit de base..HEAD, dans le clone dir, contre
// l'adhésion lue au commit optInSHA. Un commit annulé par un suivant compte :
// une fois poussé, il reste dans l'historique et peut déclencher un
// workflow. check rend un *pass.ZoneRefusalError si un commit touche une
// zone interdite ou si l'adhésion manque, et sinon les signalements des
// zones sensibles.
func check(ctx context.Context, remote gitrepo.Remote, dir, optInSHA, base string) ([]zones.Finding, error) {
	refuse := func(format string, args ...any) ([]zones.Finding, error) {
		return nil, &pass.ZoneRefusalError{Reason: fmt.Sprintf(format, args...)}
	}
	if optInSHA == "" {
		return refuse("commit de l'adhésion de référence inconnu")
	}
	// Ouvert sur un seul commit, le dépôt ne sert qu'à lire l'adhésion.
	at, err := gitdiff.Open(dir, optInSHA, optInSHA)
	if err != nil {
		return refuse("commit de base %s introuvable pour lire l'adhésion", optInSHA)
	}
	data, ok, err := at.File(optInSHA, optin.Path)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return refuse("pas d'adhésion (%s) au commit de base %s", optin.Path, optInSHA)
	}
	o, err := optin.Parse(data)
	if err != nil {
		return refuse("adhésion (%s) invalide au commit de base %s : %v", optin.Path, optInSHA, err)
	}

	out, err := remote.Git(ctx, dir, "rev-list", "--reverse", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	var flagged []zones.Finding
	for i, c := range strings.Fields(out) {
		repo, err := gitdiff.Open(dir, c+"^", c)
		if err != nil {
			return nil, err
		}
		changes, err := repo.Changes()
		if err != nil {
			return nil, err
		}
		report, err := zones.Check(o, changes, repo)
		if err != nil {
			return nil, err
		}
		for _, f := range report.Findings {
			switch f.Verdict {
			case zones.Block:
				// Le SHA réécrit par la Publication n'existe nulle part sur la
				// forge : le rang dans la série parle mieux à un humain.
				return refuse("le commit %d de la série touche %s, dans la zone interdite %s", i+1, code(f.Path), code(f.Zone))
			case zones.Flag:
				if !slices.ContainsFunc(flagged, func(g zones.Finding) bool { return g.Path == f.Path }) {
					flagged = append(flagged, f)
				}
			}
		}
	}
	return flagged, nil
}

// mention liste les chemins d'une zone sensible touchés par la série.
func mention(flagged []zones.Finding) string {
	var b strings.Builder
	b.WriteString("Zones sensibles touchées :\n")
	for _, f := range flagged {
		fmt.Fprintf(&b, "\n- %s (zone %s, %s)", code(f.Path), code(f.Zone), code(f.Reason))
	}
	return b.String()
}

// code rend un chemin écrit par l'agent en code Markdown sur une ligne : il
// ne peut ni sortir du bloc ni mentionner quelqu'un.
func code(s string) string {
	return "`" + strings.ReplaceAll(oneLine(s), "`", "'") + "`"
}

func sensitiveNote(flagged []zones.Finding) string {
	if len(flagged) == 0 {
		return ""
	}
	return "\n\n" + mention(flagged) + "\n\nUn humain décide (label `" + forge.LabelSensitiveZone + "`)."
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
