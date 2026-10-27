package weekly_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/weekly"
)

// now est un mercredi ; la semaine va du lundi 19 au dimanche 25 octobre.
var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

var from, to = weekly.Week(now)

const (
	asIs   = "feat: x\n\nNight-Shift-Agent: claude\nNight-Shift-Pass: pass-1"
	human  = "fix: retouche"
	agentB = "test: y\n\nNight-Shift-Agent: claude"
)

type world struct{ f *memforge.Forge }

func newWorld() world {
	f := memforge.New()
	f.Now = func() time.Time { return now }
	return world{f}
}

// ticket ajoute le ticket n, réservé par une passe une heure avant now, avec
// ses labels et ses commentaires de rendu.
func (w world) ticket(n int, labels []string, comments ...string) {
	cs := []forge.Comment{{Author: "night-shift", Associated: true, CreatedAt: now.Add(-time.Hour),
		Body: fmt.Sprintf("%spass-1 le %s.", pass.ReservationNote, now.Add(-time.Hour).Format(time.RFC3339))}}
	for _, c := range comments {
		cs = append(cs, forge.Comment{Author: "night-shift", Associated: true, CreatedAt: now.Add(-30 * time.Minute), Body: c})
	}
	w.f.AddIssue(forge.Ticket{Repo: "o/a", Number: n, Title: "t", Author: "alice", AuthorAssociated: true, Labels: labels, CreatedAt: from.AddDate(0, 0, -10), Comments: cs})
}

// change ouvre la demande de changement du ticket n, dans l'état donné, et
// rend son numéro.
func (w world) change(t *testing.T, n int, state forge.ChangeRequestState, at time.Time, commits ...string) int {
	t.Helper()
	cr, _, err := w.f.OpenChangeRequest(context.Background(), "o/a", forge.ChangeRequest{Head: fmt.Sprintf("agent/%d-t", n), Base: "main", Title: "t", Body: fmt.Sprintf("Closes #%d", n)})
	if err != nil {
		t.Fatal(err)
	}
	w.f.SetChange("o/a", cr, state, at, commits...)
	return cr
}

func (w world) derive(t *testing.T) weekly.Report {
	t.Helper()
	r, err := weekly.Derive(context.Background(), w.f, w.f, "o/a", from, to)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func outcomes(r weekly.Report) map[int]weekly.Ticket {
	out := map[int]weekly.Ticket{}
	for _, t := range r.Tickets {
		out[t.Number] = t
	}
	return out
}

func TestEachOutcomeIsReadFromTheForge(t *testing.T) {
	w := newWorld()
	w.ticket(1, nil)
	w.change(t, 1, forge.ChangeMerged, now, asIs, agentB)
	w.ticket(2, nil)
	w.change(t, 2, forge.ChangeMerged, now, asIs, human)
	w.ticket(3, nil)
	w.change(t, 3, forge.ChangeClosed, now)
	w.ticket(4, []string{forge.LabelHuman})
	w.ticket(5, []string{forge.LabelNeedsInfo})
	w.ticket(6, []string{forge.LabelHuman}, "night-shift (passe pass-1)"+pass.ZoneRefusalNote+" de ce tour et le ticket revient à un humain.")
	w.ticket(7, nil)
	w.change(t, 7, forge.ChangeOpen, time.Time{}, asIs)
	// La CI rouge rend le ticket, mais un humain a mergé la PR après coup.
	w.ticket(8, []string{forge.LabelHuman})
	w.change(t, 8, forge.ChangeMerged, now, asIs, human)

	got := outcomes(w.derive(t))

	for n, want := range map[int]weekly.Ticket{
		1: {Outcome: weekly.MergedAsIs},
		2: {Outcome: weekly.MergedAfterRework},
		3: {Outcome: weekly.ClosedUnmerged},
		4: {Outcome: weekly.HandedBack},
		5: {Outcome: weekly.HandedBack, NeedsInfo: true},
		6: {Outcome: weekly.HandedBack, ZoneRefusal: true},
		7: {Outcome: weekly.Pending},
		8: {Outcome: weekly.MergedAfterRework},
	} {
		g := got[n]
		if g.Outcome != want.Outcome || g.NeedsInfo != want.NeedsInfo || g.ZoneRefusal != want.ZoneRefusal {
			t.Errorf("#%d = %+v, attendu %+v", n, g, want)
		}
	}
	if got[1].ChangeRequest == "" || got[4].ChangeRequest != "" {
		t.Errorf("demandes = %q et %q, attendu l'URL de la seule demande ouverte", got[1].ChangeRequest, got[4].ChangeRequest)
	}
}

func TestDelegationRateExcludesNeedsInfo(t *testing.T) {
	w := newWorld()
	w.ticket(1, nil)
	w.change(t, 1, forge.ChangeMerged, now, asIs)
	w.ticket(2, nil)
	w.change(t, 2, forge.ChangeMerged, now, human)
	w.ticket(3, nil)
	w.change(t, 3, forge.ChangeClosed, now)
	w.ticket(4, []string{forge.LabelHuman})
	w.ticket(5, []string{forge.LabelNeedsInfo})

	r := w.derive(t)

	if r.Merged != 2 || r.Denominator != 4 || r.NeedsInfo != 1 || r.ChangeRequestsOpened != 3 {
		t.Fatalf("taux = %d/%d, needs-info %d, demandes ouvertes %d ; attendu 2/4, 1, 3", r.Merged, r.Denominator, r.NeedsInfo, r.ChangeRequestsOpened)
	}
	body := weekly.Body(r, now)
	for _, want := range []string{"**Taux de délégation : 2/4 (50 %)**", "needs-info, exclus du calcul : 1", "demandes de changement ouvertes dans la semaine : 3", "| #5 | rendu (needs-info) | aucune |"} {
		if !strings.Contains(body, want) {
			t.Errorf("corps sans %q :\n%s", want, body)
		}
	}
}

func TestOnlyReservationsOfTheWeekByAnAssociatedAuthorCount(t *testing.T) {
	w := newWorld()
	note := func(at time.Time) string {
		return fmt.Sprintf("%spass-0 le %s.", pass.ReservationNote, at.Format(time.RFC3339))
	}
	for n, c := range map[int]forge.Comment{
		1: {Author: "night-shift", Associated: true, CreatedAt: from.Add(-time.Hour), Body: note(from.Add(-time.Hour))},
		2: {Author: "mallory", CreatedAt: now.Add(-time.Hour), Body: note(now)},
		3: {Author: "night-shift", Associated: true, CreatedAt: from, Body: note(from)},
	} {
		w.f.AddIssue(forge.Ticket{Repo: "o/a", Number: n, Title: "t", Author: "alice", CreatedAt: from.AddDate(0, 0, -10),
			Labels: []string{forge.LabelHuman}, Comments: []forge.Comment{c, {Author: "bob", CreatedAt: now, Body: "relance"}}})
	}

	got := outcomes(w.derive(t))

	if _, ok := got[3]; len(got) != 1 || !ok {
		t.Fatalf("tickets = %+v, attendu le seul #3, réservé dans la semaine par un auteur associé", got)
	}
}

func TestTrustCriterionNeedsFiveConsecutiveMergesAsIs(t *testing.T) {
	for _, c := range []struct {
		asIs int
		want bool
	}{{4, false}, {5, true}} {
		t.Run(fmt.Sprint(c.asIs), func(t *testing.T) {
			w := newWorld()
			// Une retouche, plus ancienne, puis la série.
			w.change(t, 100, forge.ChangeMerged, from.AddDate(0, 0, -20), human)
			for i := range c.asIs {
				w.change(t, 101+i, forge.ChangeMerged, from.AddDate(0, 0, -10+i), asIs)
			}
			// Une demande encore ouverte n'interrompt pas la série.
			w.change(t, 200, forge.ChangeOpen, time.Time{}, human)

			r := w.derive(t)

			if r.TrustReached() != c.want || r.Streak != c.asIs {
				t.Fatalf("série = %d, palier = %t ; attendu %d, %t", r.Streak, r.TrustReached(), c.asIs, c.want)
			}
			if body := weekly.Body(r, now); strings.Contains(body, "Critère de palier de confiance atteint") != c.want {
				t.Fatalf("corps :\n%s", body)
			}
		})
	}
}

func TestTwoRunsInTheSameWeekUpdateOneIssue(t *testing.T) {
	w := newWorld()
	w.ticket(1, nil)
	cr := w.change(t, 1, forge.ChangeOpen, time.Time{}, asIs)
	ctx := context.Background()

	first, err := weekly.Publish(ctx, w.f, w.derive(t), now)
	if err != nil {
		t.Fatal(err)
	}
	w.f.SetChange("o/a", cr, forge.ChangeMerged, now, asIs)
	second, err := weekly.Publish(ctx, w.f, w.derive(t), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	issues := w.f.CreatedIssues("o/a")
	if first != second || len(issues) != 1 {
		t.Fatalf("issues = %d (%s, %s), attendu une seule issue pour la semaine", len(issues), first, second)
	}
	if i := issues[0]; i.Title != "night-shift : semaine du 2026-10-19 au 2026-10-25" || !strings.Contains(i.Body, "mergé tel quel") || !strings.Contains(i.Body, "1/1") {
		t.Fatalf("issue = %q\n%s", i.Title, i.Body)
	}
}

func TestWeekRunsFromMondayToMonday(t *testing.T) {
	for _, d := range []time.Time{
		time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 23, 59, 0, 0, time.UTC),
	} {
		f, e := weekly.Week(d)
		if !f.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("semaine de %s = %s → %s", d, f, e)
		}
	}
}
