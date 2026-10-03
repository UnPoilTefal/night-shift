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
}

// Report est le rapport d'une passe.
type Report struct {
	ID      string
	Repos   []RepoReport
	Tickets []TicketReport
}

// Run exécute une passe.
func Run(ctx context.Context, cfg Config, f forge.Forge, h harness.Harness) (Report, error) {
	r := Report{ID: cfg.ID}
	var queue []forge.Ticket
	perRepoCap := map[string]int{}

	for _, repo := range cfg.Repos {
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

	taken := map[string]int{}
	for _, t := range queue {
		if len(r.Tickets) >= cfg.MaxTickets {
			break
		}
		if taken[t.Repo] >= perRepoCap[t.Repo] {
			continue
		}
		tr, err := process(ctx, cfg, f, h, t)
		if err != nil {
			return r, err
		}
		taken[t.Repo]++
		r.Tickets = append(r.Tickets, tr)
	}
	return r, nil
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

// process réserve un ticket, le confie au harness et le rend dans un état
// explicite : aucun ticket ne reste en agent-in-progress.
func process(ctx context.Context, cfg Config, f forge.Forge, h harness.Harness, t forge.Ticket) (TicketReport, error) {
	if err := reserve(ctx, cfg, f, t); err != nil {
		return TicketReport{}, fmt.Errorf("réservation de %s#%d : %w", t.Repo, t.Number, err)
	}

	res, err := h.Run(ctx, t)
	if err != nil {
		res = harness.Result{Outcome: harness.Failed, Reason: err.Error()}
	}

	var label, comment string
	switch res.Outcome {
	case harness.Succeeded:
		comment = fmt.Sprintf("night-shift (passe %s) : travail terminé. %s", cfg.ID, res.Reason)
	case harness.Stopped:
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : arrêt motivé de l'agent, ticket rendu à un humain.\n\nMotif : %s", cfg.ID, res.Reason)
	default:
		res.Outcome = harness.Failed
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : traitement interrompu, ticket rendu à un humain.\n\nCause : %s", cfg.ID, res.Reason)
	}

	if err := release(ctx, f, t, label, comment); err != nil {
		return TicketReport{}, fmt.Errorf("rendu de %s#%d : %w", t.Repo, t.Number, err)
	}
	return TicketReport{Repo: t.Repo, Number: t.Number, Title: t.Title, Outcome: res.Outcome, Reason: res.Reason}, nil
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
