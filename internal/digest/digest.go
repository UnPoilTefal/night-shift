// Package digest rend compte d'une passe à la relecture humaine : une issue
// de digest par dépôt adhérent, puis une notification courte qui renvoie vers
// ces issues. Rien de ce qui ne concerne pas un dépôt n'y sort : ni les
// dépôts écartés, ni le détail des incidents, qui restent dans l'état interne
// de la passe (journaux du job). La notification ne porte que des compteurs
// et des liens.
package digest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/pass"
)

// Notifier envoie une notification courte hors de la forge (Discord…).
type Notifier interface {
	Notify(ctx context.Context, text string) error
}

// Issue est une issue de digest ouverte sur un dépôt.
type Issue struct {
	Repo   string
	Number int
	URL    string
}

// Result dit ce que le digest a produit.
type Result struct {
	Issues []Issue
	// Notified vaut true si la notification est partie.
	Notified bool
	// NotifyErr est l'échec de la notification, signalé sur chaque issue de
	// digest sans faire échouer la passe.
	NotifyErr error
}

// Publish ouvre une issue de digest sur chaque dépôt parcouru par la passe ;
// un dépôt écarté (adhésion absente ou invalide) ou injoignable n'en reçoit
// pas. Elle notifie ensuite n, s'il est configuré. L'erreur rendue ne
// concerne que les issues.
func Publish(ctx context.Context, f forge.Forge, n Notifier, r pass.Report) (Result, error) {
	var res Result
	var errs []error
	failed := 0
	for _, rr := range r.Repos {
		if rr.Status != pass.Eligible {
			continue
		}
		number, url, err := f.CreateIssue(ctx, rr.Repo, "night-shift : digest de la passe "+r.ID, Body(r, rr.Repo, n != nil))
		if err != nil {
			errs = append(errs, fmt.Errorf("digest de %s : %w", rr.Repo, err))
			failed++
			continue
		}
		res.Issues = append(res.Issues, Issue{Repo: rr.Repo, Number: number, URL: url})
	}

	if n != nil {
		if err := n.Notify(ctx, Message(r, res.Issues, failed)); err != nil {
			res.NotifyErr = err
			for _, i := range res.Issues {
				if cerr := f.Comment(ctx, i.Repo, i.Number, "night-shift : la notification Discord a échoué : "+err.Error()); cerr != nil {
					errs = append(errs, fmt.Errorf("digest de %s : %w", i.Repo, cerr))
				}
			}
		} else {
			res.Notified = true
		}
	}
	return res, errors.Join(errs...)
}

// Body rend le corps de l'issue de digest d'un dépôt parcouru. Les tickets
// n'y sont désignés que par leur numéro : leur contenu reste sur la forge.
func Body(r pass.Report, repo string, notifier bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Digest de la passe %s sur %s.\n\n", r.ID, repo)

	tickets, retriaged := byRepo(r, repo)
	if len(tickets) == 0 && len(retriaged) == 0 {
		b.WriteString("Aucun ticket prêt éligible : la passe n'a rien traité dans ce dépôt.\n\n")
	}
	var cost float64
	if len(tickets) > 0 {
		fmt.Fprintf(&b, "## Tickets traités (%d)\n\n| Ticket | Résultat provisoire | PR | Durée | Coût |\n|---|---|---|---|---|\n", len(tickets))
		for _, t := range tickets {
			pr := t.PullRequest
			if pr == "" {
				pr = "–"
			}
			fmt.Fprintf(&b, "| #%d | %s | %s | %s | %.2f $ |\n", t.Number, pass.ProvisionalOutcome(t.Outcome), pr, t.Duration.Round(time.Second), t.CostUSD)
			cost += t.CostUSD
		}
		b.WriteString("\nLe détail est en commentaire de chaque ticket.\n\n")
	}
	if len(retriaged) > 0 {
		fmt.Fprintf(&b, "## Tickets sortis de la file (%d)\n\n", len(retriaged))
		for _, t := range retriaged {
			fmt.Fprintf(&b, "- #%d : repassé en `%s`, %s.\n", t.Number, forge.LabelTriage, t.Reason)
		}
		b.WriteString("\n")
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(&b, "**La passe a connu %d incident(s)** : une partie n'a pas abouti. Le détail reste dans l'état interne de la passe.\n\n", len(r.Errors))
	}
	if r.Started.IsZero() || r.Finished.Before(r.Started) {
		fmt.Fprintf(&b, "Coût des agents : %.2f $.\n\n", cost)
	} else {
		fmt.Fprintf(&b, "Durée de la passe : %s, coût des agents : %.2f $.\n\n", r.Finished.Sub(r.Started).Round(time.Second), cost)
	}

	if notifier {
		b.WriteString("Notification Discord : envoyée après l'ouverture de ce digest ; un échec y sera signalé en commentaire.\n")
	} else {
		b.WriteString("Notification Discord : non configurée (aucun webhook fourni).\n")
	}
	return b.String()
}

// Message rend la notification d'une passe : des compteurs et les liens vers
// les issues de digest, sans contenu de ticket, nom de dépôt écarté ni
// détail d'incident. failed compte les issues de digest qui n'ont pas pu être
// ouvertes.
func Message(r pass.Report, issues []Issue, failed int) string {
	msg := fmt.Sprintf("night-shift · passe %s : %s, %d sorti(s) de la file", r.ID, treated(len(r.Tickets)), len(r.Retriaged))
	excluded, down := 0, 0
	for _, rr := range r.Repos {
		switch rr.Status {
		case pass.NoOptIn, pass.InvalidOptIn:
			excluded++
		case pass.Unreachable:
			down++
		}
	}
	for _, c := range []struct {
		n    int
		what string
	}{
		{len(r.Errors), "incident(s)"},
		{excluded, "dépôt(s) écarté(s)"},
		{down, "dépôt(s) injoignable(s)"},
		{failed, "digest(s) impossible(s) à ouvrir"},
	} {
		if c.n > 0 {
			msg += fmt.Sprintf(", %d %s", c.n, c.what)
		}
	}
	msg += "."
	if len(issues) == 0 {
		return msg + " Aucun digest ouvert."
	}
	var links []string
	for _, i := range issues {
		links = append(links, i.URL)
	}
	return msg + " Digest : " + strings.Join(links, " ")
}

func treated(n int) string {
	switch n {
	case 0:
		return "aucun ticket traité"
	case 1:
		return "1 ticket traité"
	default:
		return fmt.Sprintf("%d tickets traités", n)
	}
}

func byRepo(r pass.Report, repo string) ([]pass.TicketReport, []pass.RetriageReport) {
	var ts []pass.TicketReport
	for _, t := range r.Tickets {
		if t.Repo == repo {
			ts = append(ts, t)
		}
	}
	var rs []pass.RetriageReport
	for _, t := range r.Retriaged {
		if t.Repo == repo {
			rs = append(rs, t)
		}
	}
	return ts, rs
}
