// Package pass est l'orchestrateur d'une passe : il parcourt la file des
// tickets prêts des dépôts adhérents, en réserve au plus un nombre plafonné,
// les confie au harness et rend chacun dans un état explicite. Il ne connaît
// la forge et l'agent qu'à travers leurs adaptateurs.
package pass

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/optin"
)

// Config paramètre une passe.
type Config struct {
	// Repos liste les dépôts candidats ; seuls ceux qui portent une adhésion
	// valide sont parcourus.
	Repos []string
	// MaxTickets plafonne le nombre de tickets traités par la passe.
	MaxTickets int
	// ID identifie la passe dans les commentaires et le rapport.
	ID string
	// Now est l'horloge de la passe.
	Now func() time.Time
}

// RepoStatus dit si un dépôt candidat a été parcouru.
type RepoStatus string

// Statuts possibles d'un dépôt candidat.
const (
	Eligible     RepoStatus = "eligible"
	NoOptIn      RepoStatus = "no-opt-in"
	InvalidOptIn RepoStatus = "invalid-opt-in"
	Unreachable  RepoStatus = "unreachable"
)

// RepoReport est le constat de la passe sur un dépôt candidat.
type RepoReport struct {
	Repo   string
	Status RepoStatus
	Detail string
}

// TicketReport est le constat de la passe sur un ticket traité.
type TicketReport struct {
	Repo    string
	Number  int
	Title   string
	Outcome harness.Outcome
	Reason  string
	// PullRequest est l'URL de la PR en brouillon, en cas de succès.
	PullRequest string
	CostUSD     float64
	Duration    time.Duration
	Transcript  string
}

// RetriageReport est le constat d'un ticket sorti de la file sans être
// traité, faute d'un brief que seul du contenu de confiance fonde.
type RetriageReport struct {
	Repo   string
	Number int
	Reason string
}

// Report est le rapport d'une passe.
type Report struct {
	ID      string
	Repos   []RepoReport
	Tickets []TicketReport
	// Retriaged liste les tickets repassés en needs-triage.
	Retriaged []RetriageReport
}

// Publisher est l'étape de Publication : elle applique la série de commits
// de l'agent sur une branche agent/, la pousse et ouvre la PR en brouillon,
// dont elle rend l'URL. C'est la seule étape qui écrit du code sur la forge.
type Publisher interface {
	Publish(ctx context.Context, task harness.Task, res harness.Result) (pullRequest string, err error)
}

// Run exécute une passe complète dans un seul processus : sélection et
// réservation, travail de l'agent, puis Publication et rendu de chaque
// ticket. Le palier solo répartit ces étapes entre trois conteneurs (Select,
// harness, Settle) pour que l'agent ne côtoie jamais le jeton de forge.
func Run(ctx context.Context, cfg Config, f forge.Forge, h harness.Harness, p Publisher) (Report, error) {
	r, tasks, err := Select(ctx, cfg, f)
	if err != nil {
		for _, t := range tasks {
			if tr, serr := Settle(ctx, cfg, f, p, t, harness.Result{Outcome: harness.Failed, Reason: "passe interrompue avant le travail de l'agent"}); serr == nil {
				r.Tickets = append(r.Tickets, tr)
			}
		}
		return r, err
	}
	for _, t := range tasks {
		res, err := h.Run(ctx, t)
		if err != nil {
			res = harness.Result{Outcome: harness.Failed, Reason: err.Error()}
		}
		tr, err := Settle(ctx, cfg, f, p, t, res)
		if err != nil {
			return r, err
		}
		r.Tickets = append(r.Tickets, tr)
	}
	return r, nil
}

// Select parcourt la file des dépôts adhérents et réserve au plus
// cfg.MaxTickets tickets, dans l'ordre priorité puis ancienneté. Chaque tâche
// rendue est réservée et doit être rendue par Settle, y compris quand Select
// rend aussi une erreur. Un ticket dont le brief ne repose pas sur du seul
// contenu de confiance n'est pas réservé : il repasse en needs-triage.
func Select(ctx context.Context, cfg Config, f forge.Forge) (Report, []harness.Task, error) {
	r := Report{ID: cfg.ID}
	var queue []forge.Ticket
	perRepoCap := map[string]int{}
	trustedAuthors := map[string][]string{}

	for _, repo := range dedupe(cfg.Repos) {
		o, rr := readOptIn(ctx, f, repo)
		r.Repos = append(r.Repos, rr)
		if rr.Status != Eligible {
			continue
		}
		ts, err := f.ReadyTickets(ctx, repo)
		if err != nil {
			r.Repos[len(r.Repos)-1] = RepoReport{Repo: repo, Status: Unreachable, Detail: err.Error()}
			continue
		}
		perRepoCap[repo] = o.TrustLevel.TicketsPerPass
		trustedAuthors[repo] = o.TrustedAuthors
		for _, t := range ts {
			if t.OpenBlockers == 0 {
				queue = append(queue, t)
			}
		}
	}

	slices.SortStableFunc(queue, func(a, b forge.Ticket) int {
		return cmp.Or(
			cmp.Compare(priority(a), priority(b)),
			a.CreatedAt.Compare(b.CreatedAt),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.Number, b.Number),
		)
	})

	var tasks []harness.Task
	taken := map[string]int{}
	for _, t := range queue {
		if len(tasks) >= cfg.MaxTickets {
			break
		}
		if taken[t.Repo] >= perRepoCap[t.Repo] {
			continue
		}
		brief, reason := Brief(t, trustedAuthors[t.Repo])
		if reason != "" {
			if err := retriage(ctx, cfg, f, t, reason); err != nil {
				return r, tasks, fmt.Errorf("retri de %s#%d : %w", t.Repo, t.Number, err)
			}
			r.Retriaged = append(r.Retriaged, RetriageReport{Repo: t.Repo, Number: t.Number, Reason: reason})
			continue
		}
		if err := reserve(ctx, cfg, f, t); err != nil {
			rollback(ctx, f, t)
			return r, tasks, fmt.Errorf("réservation de %s#%d : %w", t.Repo, t.Number, err)
		}
		taken[t.Repo]++
		tasks = append(tasks, harness.Task{Ticket: relayed(t, trustedAuthors[t.Repo]), Brief: brief, PassID: cfg.ID})
	}
	return r, tasks, nil
}

// Brief construit le brief transmis à l'agent à partir du seul contenu
// d'auteurs de confiance : le titre et le corps si l'auteur du ticket l'est,
// puis les commentaires de confiance dans l'ordre, hors ceux de night-shift.
// Le reste est écarté. Le brief date du dernier contenu de confiance. Si
// aucun contenu n'est de confiance, ou si un tiers a écrit ou modifié un
// commentaire depuis, Brief rend le motif pour lequel le ticket doit être
// retrié par un humain.
func Brief(t forge.Ticket, trustedAuthors []string) (brief, retriage string) {
	var b strings.Builder
	var last time.Time
	if trusted(t.Author, t.AuthorAssociated, trustedAuthors) {
		b.WriteString("# " + t.Title + "\n\n" + t.Body + "\n")
		last = t.CreatedAt
	}
	for _, c := range t.Comments {
		if !trusted(c.Author, c.Associated, trustedAuthors) {
			continue
		}
		last = later(last, c.CreatedAt)
		// Les comptes rendus des passes précédentes datent le brief sans en
		// faire partie.
		if !strings.HasPrefix(c.Body, commentPrefix) {
			fmt.Fprintf(&b, "\n---\n\nCommentaire de %s :\n\n%s\n", c.Author, c.Body)
		}
	}
	if b.Len() == 0 {
		return "", "aucun contenu du ticket n'est écrit par un auteur de confiance"
	}
	for _, c := range t.Comments {
		if !trusted(c.Author, c.Associated, trustedAuthors) && !later(c.CreatedAt, c.UpdatedAt).Before(last) {
			return "", fmt.Sprintf("un commentaire de `%s`, auteur hors confiance, est postérieur au brief", c.Author)
		}
	}
	return b.String(), ""
}

// trusted dit si un auteur est de confiance : associé au dépôt, ou nommé par
// l'adhésion. Les forges ignorent la casse des identifiants.
func trusted(author string, associated bool, trustedAuthors []string) bool {
	return associated || slices.ContainsFunc(trustedAuthors, func(a string) bool { return strings.EqualFold(a, author) })
}

// later rend la plus récente de deux dates.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// relayed rend le ticket tel qu'il est relayé jusqu'au conteneur de
// l'agent : sans corps ni commentaires, et sans titre si son auteur n'est
// pas de confiance. Le brief reste le seul contenu du ticket qu'il reçoit.
func relayed(t forge.Ticket, trustedAuthors []string) forge.Ticket {
	t.Body, t.Comments = "", nil
	if !trusted(t.Author, t.AuthorAssociated, trustedAuthors) {
		t.Title = ""
	}
	return t
}

func readOptIn(ctx context.Context, f forge.Forge, repo string) (optin.OptIn, RepoReport) {
	data, ok, err := f.File(ctx, repo, optin.Path)
	switch {
	case err != nil:
		return optin.OptIn{}, RepoReport{Repo: repo, Status: Unreachable, Detail: err.Error()}
	case !ok:
		return optin.OptIn{}, RepoReport{Repo: repo, Status: NoOptIn}
	}
	o, err := optin.Parse(data)
	if err != nil {
		return optin.OptIn{}, RepoReport{Repo: repo, Status: InvalidOptIn, Detail: err.Error()}
	}
	return o, RepoReport{Repo: repo, Status: Eligible}
}

// priority rend 0 à 3 pour prio:P0 à prio:P3, et 4 sans label de priorité.
func priority(t forge.Ticket) int {
	best := 4
	for _, l := range t.Labels {
		if p, ok := strings.CutPrefix(l, "prio:P"); ok && len(p) == 1 && p[0] >= '0' && p[0] <= '3' {
			best = min(best, int(p[0]-'0'))
		}
	}
	return best
}

// commentPrefix ouvre chaque commentaire que night-shift poste sur un
// ticket.
const commentPrefix = "night-shift "

// cleanupTimeout borne les appels à la forge qui rendent un ticket : ils
// doivent aboutir même si la passe a été annulée ou a dépassé sa durée.
const cleanupTimeout = time.Minute

// Settle publie le travail de l'agent sur un ticket réservé, puis le rend
// dans un état explicite : aucun ticket ne reste en agent-in-progress, sauf
// si la forge refuse aussi le rendu, ce que l'erreur signale. Sans Publisher,
// un succès ne peut pas être publié et le ticket est rendu à un humain.
func Settle(ctx context.Context, cfg Config, f forge.Forge, p Publisher, task harness.Task, res harness.Result) (TicketReport, error) {
	t := task.Ticket
	tr := TicketReport{
		Repo: t.Repo, Number: t.Number, Title: t.Title,
		CostUSD: res.CostUSD, Duration: res.Duration, Transcript: res.Transcript,
	}

	if res.Outcome == harness.Succeeded {
		switch {
		case len(res.Patches) == 0:
			res.Outcome, res.Reason = harness.Failed, "l'agent annonce un succès sans aucun commit"
		case p == nil:
			res.Outcome, res.Reason = harness.Failed, "aucune Publication n'est configurée"
		default:
			url, err := p.Publish(ctx, task, res)
			if err != nil {
				res.Outcome, res.Reason = harness.Failed, "Publication échouée : "+err.Error()
			}
			tr.PullRequest = url
		}
	}

	var label, comment string
	switch res.Outcome {
	case harness.Succeeded:
		// Machine d'états de la spec : en cas de succès, le ticket est relié
		// à sa PR en brouillon et ne porte plus de label de cycle de vie.
		comment = fmt.Sprintf("night-shift (passe %s) : travail terminé, PR en brouillon à relire : %s\n\n%s%s",
			cfg.ID, tr.PullRequest, res.Reason, accounting(res))
	case harness.NeedsInfo:
		label = forge.LabelNeedsInfo
		comment = fmt.Sprintf("night-shift (passe %s) : l'agent s'est arrêté, il lui manque une information.\n\nMotif : %s%s",
			cfg.ID, res.Reason, accounting(res))
	case harness.Stopped:
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : arrêt motivé de l'agent, ticket rendu à un humain.\n\nMotif : %s%s",
			cfg.ID, res.Reason, accounting(res))
	default:
		res.Outcome = harness.Failed
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : traitement interrompu, ticket rendu à un humain.\n\nCause : %s%s",
			cfg.ID, res.Reason, accounting(res))
	}
	tr.Outcome, tr.Reason = res.Outcome, res.Reason

	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	if err := release(cctx, f, t, label, comment); err != nil {
		return TicketReport{}, fmt.Errorf("rendu de %s#%d, le ticket peut rester en %s : %w", t.Repo, t.Number, forge.LabelInProgress, err)
	}
	return tr, nil
}

// accounting résume le coût, la durée et la session de l'agent, quand il
// les a rapportés.
func accounting(res harness.Result) string {
	if res.Duration == 0 && res.CostUSD == 0 && res.SessionID == "" {
		return ""
	}
	return fmt.Sprintf("\n\nDurée : %s, coût : %.2f $, session : %s", res.Duration.Round(time.Second), res.CostUSD, res.SessionID)
}

func reserve(ctx context.Context, cfg Config, f forge.Forge, t forge.Ticket) error {
	if err := f.AddLabel(ctx, t.Repo, t.Number, forge.LabelInProgress); err != nil {
		return err
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelReady); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, fmt.Sprintf(
		"night-shift : ticket réservé par la passe %s le %s.", cfg.ID, cfg.Now().UTC().Format(time.RFC3339)))
}

// retriage sort un ticket de la file sans le traiter : il repasse en
// needs-triage, avec un commentaire qui explique pourquoi sans citer le
// contenu en cause.
func retriage(ctx context.Context, cfg Config, f forge.Forge, t forge.Ticket, reason string) error {
	if err := f.AddLabel(ctx, t.Repo, t.Number, forge.LabelTriage); err != nil {
		return err
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelReady); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, fmt.Sprintf(
		"night-shift (passe %s) : ticket non traité et sorti de la file : %s. "+
			"L'agent ne reçoit que le contenu d'auteurs de confiance ; à un humain de juger si le brief doit changer, puis de remettre le ticket en %s.",
		cfg.ID, reason, forge.LabelReady))
}

// rollback remet au mieux un ticket dans la file après une réservation
// partielle ; ses erreurs sont ignorées, celle de la réservation fait foi.
func rollback(ctx context.Context, f forge.Forge, t forge.Ticket) {
	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	_ = f.AddLabel(cctx, t.Repo, t.Number, forge.LabelReady)
	_ = f.RemoveLabel(cctx, t.Repo, t.Number, forge.LabelInProgress)
}

func release(ctx context.Context, f forge.Forge, t forge.Ticket, label, comment string) error {
	if label != "" {
		if err := f.AddLabel(ctx, t.Repo, t.Number, label); err != nil {
			return err
		}
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelInProgress); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, comment)
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// dedupe retire les dépôts en double, sans tenir compte de la casse comme
// les forges.
func dedupe(repos []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range repos {
		k := strings.ToLower(r)
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}
