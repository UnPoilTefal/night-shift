// Package weekly dérive de la forge seule le Résultat de chaque ticket
// qu'une passe a traité dans la semaine, et publie par dépôt cible une issue
// hebdomadaire : taux de délégation, Résultats et critère de palier de
// confiance. Il ne garde aucun état : chaque exécution relit tout dans la
// forge, et la même semaine met à jour la même issue. Il ne change jamais
// l'adhésion ; la décision de relever le palier reste humaine.
package weekly

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/publication"
)

// Outcome est le Résultat d'un ticket traité, lu sur la forge.
type Outcome string

// Résultats possibles.
const (
	// MergedAsIs : la demande de changement est mergée, et chacun de ses
	// commits porte le trailer de l'agent.
	MergedAsIs Outcome = "merged-as-is"
	// MergedAfterRework : elle est mergée, et au moins un commit n'a pas ce
	// trailer.
	MergedAfterRework Outcome = "merged-after-rework"
	// ClosedUnmerged : elle est fermée sans merge.
	ClosedUnmerged Outcome = "closed-unmerged"
	// HandedBack : le ticket est en needs-info ou en ready-for-human.
	HandedBack Outcome = "handed-back"
	// Pending : la demande est encore ouverte, ou le ticket encore en
	// cours ; il ne compte pas dans le taux, et s'affiche à part.
	Pending Outcome = "pending"
)

// Label distingue l'issue hebdomadaire, que PutIssue retrouve par lui et par
// son titre.
const Label = "night-shift-weekly"

// TrustStreak est le nombre de demandes de changement consécutives mergées
// telles quelles qu'exige le critère de palier de confiance.
const TrustStreak = 5

// Ticket est le Résultat d'un ticket réservé dans la semaine.
type Ticket struct {
	Number  int
	Outcome Outcome
	// NeedsInfo et ZoneRefusal précisent un ticket rendu.
	NeedsInfo, ZoneRefusal bool
	// ChangeRequest est l'URL de sa demande de changement, s'il en a une.
	ChangeRequest string
}

// Report est le constat de la semaine sur un dépôt.
type Report struct {
	Repo     string
	From, To time.Time
	Tickets  []Ticket
	// Merged sur Denominator est le taux de délégation : les tickets dont la
	// demande de changement est mergée, sur les tickets réservés hors
	// needs-info et hors attente, comptés à part dans NeedsInfo et Pending.
	Merged, Denominator, NeedsInfo int
	Pending                        int
	// ChangeRequestsOpened compte les demandes de changement de l'agent
	// ouvertes dans la semaine.
	ChangeRequestsOpened int
	// Streak compte, des plus récentes aux plus anciennes, les demandes de
	// changement closes consécutives mergées telles quelles, au plus
	// TrustStreak.
	Streak int
}

// TrustReached dit si le critère de palier de confiance est atteint.
func (r Report) TrustReached() bool { return r.Streak >= TrustStreak }

// Week rend la semaine ISO, en UTC, qui contient t : du lundi 0 h au lundi
// suivant.
func Week(t time.Time) (from, to time.Time) {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	from = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	return from, from.AddDate(0, 0, 7)
}

var agentTrailer = regexp.MustCompile(`(?m)^` + publication.AgentTrailer + `:[ \t]*\S`)

// Derive lit sur la forge les tickets de repo réservés dans [from, to) et
// leur Résultat. Un ticket est réservé par le commentaire de réservation
// d'une passe, signé d'un auteur associé au dépôt ; sa demande de changement
// est celle de sa branche agent/<n>-, ouverte après la réservation.
func Derive(ctx context.Context, src forge.TicketSource, code forge.CodeForge, repo string, from, to time.Time) (Report, error) {
	r := Report{Repo: repo, From: from, To: to}
	tickets, err := src.UpdatedTickets(ctx, repo, from)
	if err != nil {
		return r, err
	}
	crs, err := code.AgentChangeRequests(ctx, repo)
	if err != nil {
		return r, err
	}
	asIs := map[int]bool{}
	mergedAsIs := func(cr forge.ChangeRequestStatus) (bool, error) {
		if cr.State != forge.ChangeMerged {
			return false, nil
		}
		if ok, seen := asIs[cr.Number]; seen {
			return ok, nil
		}
		msgs, err := code.ChangeRequestCommits(ctx, repo, cr.Number)
		if err != nil {
			return false, err
		}
		ok := len(msgs) > 0 && !slices.ContainsFunc(msgs, func(m string) bool { return !agentTrailer.MatchString(m) })
		asIs[cr.Number] = ok
		return ok, nil
	}

	for _, t := range tickets {
		reserved, ok := reservation(t, from, to)
		if !ok {
			continue
		}
		tk := Ticket{Number: t.Number, Outcome: Pending}
		cr, hasCR := changeRequest(crs, t.Number, reserved)
		if hasCR {
			tk.ChangeRequest = cr.URL
		}
		switch {
		case hasCR && cr.State == forge.ChangeMerged:
			ok, err := mergedAsIs(cr)
			if err != nil {
				return r, err
			}
			tk.Outcome = MergedAfterRework
			if ok {
				tk.Outcome = MergedAsIs
			}
		case hasCR && cr.State == forge.ChangeClosed:
			tk.Outcome = ClosedUnmerged
		case slices.Contains(t.Labels, forge.LabelNeedsInfo):
			tk.Outcome, tk.NeedsInfo = HandedBack, true
		case slices.Contains(t.Labels, forge.LabelHuman):
			tk.Outcome, tk.ZoneRefusal = HandedBack, zoneRefusal(t, reserved)
		}
		r.Tickets = append(r.Tickets, tk)
		switch {
		case tk.NeedsInfo:
			r.NeedsInfo++
			continue
		case tk.Outcome == Pending:
			r.Pending++
			continue
		case tk.Outcome == MergedAsIs || tk.Outcome == MergedAfterRework:
			r.Merged++
		}
		r.Denominator++
	}
	slices.SortFunc(r.Tickets, func(a, b Ticket) int { return a.Number - b.Number })

	for _, cr := range crs {
		if !cr.CreatedAt.Before(from) && cr.CreatedAt.Before(to) {
			r.ChangeRequestsOpened++
		}
	}
	closed := slices.DeleteFunc(slices.Clone(crs), func(cr forge.ChangeRequestStatus) bool { return cr.State == forge.ChangeOpen })
	slices.SortStableFunc(closed, func(a, b forge.ChangeRequestStatus) int { return b.ClosedAt.Compare(a.ClosedAt) })
	for _, cr := range closed {
		if r.Streak == TrustStreak {
			break
		}
		ok, err := mergedAsIs(cr)
		if err != nil {
			return r, err
		}
		if !ok {
			break
		}
		r.Streak++
	}
	return r, nil
}

// reservation rend la date de la dernière réservation du ticket dans
// [from, to), signée d'un auteur associé au dépôt.
func reservation(t forge.Ticket, from, to time.Time) (time.Time, bool) {
	var at time.Time
	for _, c := range t.Comments {
		if c.Associated && strings.HasPrefix(c.Body, pass.ReservationNote) && !c.CreatedAt.Before(from) && c.CreatedAt.Before(to) && c.CreatedAt.After(at) {
			at = c.CreatedAt
		}
	}
	return at, !at.IsZero()
}

// changeRequest rend la plus récente demande de changement de la branche
// agent/<n>- ouverte après la réservation ; crs va de la plus récente à la
// plus ancienne.
func changeRequest(crs []forge.ChangeRequestStatus, number int, reserved time.Time) (forge.ChangeRequestStatus, bool) {
	prefix := fmt.Sprintf("%s%d-", forge.AgentBranchPrefix, number)
	for _, cr := range crs {
		if strings.HasPrefix(cr.Head, prefix) && !cr.CreatedAt.Before(reserved) {
			return cr, true
		}
	}
	return forge.ChangeRequestStatus{}, false
}

// zoneRefusal dit si un commentaire de rendu postérieur à la réservation
// rapporte un refus de zone.
func zoneRefusal(t forge.Ticket, reserved time.Time) bool {
	return slices.ContainsFunc(t.Comments, func(c forge.Comment) bool {
		return c.Associated && !c.CreatedAt.Before(reserved) && strings.Contains(c.Body, pass.ZoneRefusalNote)
	})
}

// Title est le titre de l'issue hebdomadaire de la semaine qui commence à
// from.
func Title(from time.Time) string {
	return fmt.Sprintf("night-shift : semaine du %s au %s", from.Format(time.DateOnly), from.AddDate(0, 0, 6).Format(time.DateOnly))
}

// Body rend le corps de l'issue hebdomadaire, lu sur la forge à now.
func Body(r Report, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Résultats des tickets que night-shift a réservés sur %s du lundi %s au dimanche %s (UTC), lus sur la forge le %s. "+
		"Cette issue se met à jour à chaque exécution de `night-shift weekly` dans la semaine.\n\n",
		r.Repo, r.From.Format(time.DateOnly), r.From.AddDate(0, 0, 6).Format(time.DateOnly), now.UTC().Format("2006-01-02 15:04 UTC"))
	rate := "aucun ticket à compter"
	if r.Denominator > 0 {
		rate = fmt.Sprintf("%d/%d (%d %%)", r.Merged, r.Denominator, 100*r.Merged/r.Denominator)
	}
	fmt.Fprintf(&b, "**Taux de délégation : %s** : part des tickets réservés dont la demande de changement a été mergée par un humain, telle quelle ou après retouches.\n\n", rate)
	fmt.Fprintf(&b, "- %s, exclus du calcul : %d\n- en attente : %d\n- demandes de changement ouvertes dans la semaine : %d\n\n", forge.LabelNeedsInfo, r.NeedsInfo, r.Pending, r.ChangeRequestsOpened)
	if len(r.Tickets) == 0 {
		b.WriteString("Aucun ticket réservé cette semaine.\n\n")
	} else {
		b.WriteString("| Ticket | Résultat | Demande de changement |\n|---|---|---|\n")
		for _, t := range r.Tickets {
			cr := t.ChangeRequest
			if cr == "" {
				cr = "aucune"
			}
			fmt.Fprintf(&b, "| #%d | %s | %s |\n", t.Number, describe(t), cr)
		}
		b.WriteString("\n")
	}
	if r.TrustReached() {
		fmt.Fprintf(&b, "**Critère de palier de confiance atteint** : les %d dernières demandes de changement closes de l'agent ont été mergées telles quelles. "+
			"La décision de relever le palier reste humaine : rien n'a changé dans l'adhésion.\n", TrustStreak)
	} else {
		fmt.Fprintf(&b, "Palier de confiance : %d demande(s) de changement close(s) consécutive(s) mergée(s) telle(s) quelle(s), sur les %d qu'exige le critère.\n", r.Streak, TrustStreak)
	}
	return b.String()
}

func describe(t Ticket) string {
	switch t.Outcome {
	case MergedAsIs:
		return "mergé tel quel"
	case MergedAfterRework:
		return "mergé après retouches"
	case ClosedUnmerged:
		return "fermé sans merge"
	case HandedBack:
		switch {
		case t.NeedsInfo:
			return "rendu (" + forge.LabelNeedsInfo + ")"
		case t.ZoneRefusal:
			return "rendu, refus de zone (" + forge.LabelHuman + ")"
		}
		return "rendu (" + forge.LabelHuman + ")"
	}
	return "en attente"
}

// Publish ouvre l'issue hebdomadaire de la semaine, ou met à jour celle
// qu'une exécution précédente a ouverte, et rend son URL.
func Publish(ctx context.Context, src forge.TicketSource, r Report, now time.Time) (string, error) {
	_, url, err := src.PutIssue(ctx, r.Repo, Label, Title(r.From), Body(r, now))
	return url, err
}
