package pass_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/pass"
)

const optIn = `version: 1
trustLevel:
  ticketsPerPass: 1
  pullRequests: draft
`

var now = time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)

func day(n int) time.Time { return now.AddDate(0, 0, -n) }

// spyHarness enregistre les tickets reçus et rend le résultat configuré.
type spyHarness struct {
	result harness.Result
	err    error
	seen   []int
}

func (h *spyHarness) Run(_ context.Context, t forge.Ticket) (harness.Result, error) {
	h.seen = append(h.seen, t.Number)
	return h.result, h.err
}

func stopped() *spyHarness {
	return &spyHarness{result: harness.Result{Outcome: harness.Stopped, Reason: "brief insuffisant"}}
}

func ticket(repo string, number int, created time.Time, labels ...string) forge.Ticket {
	return forge.Ticket{
		Repo: repo, Number: number, Title: "ticket", Author: "alice",
		CreatedAt: created, Labels: append([]string{forge.LabelReady}, labels...),
	}
}

func runPass(t *testing.T, f *memforge.Forge, h harness.Harness, repos ...string) pass.Report {
	t.Helper()
	r, err := pass.Run(context.Background(), pass.Config{
		Repos: repos, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
	}, f, h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPicksHighestPriorityThenOldest(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(30), "prio:P3"))
	f.AddIssue(ticket("o/a", 2, day(5), "prio:P1"))
	f.AddIssue(ticket("o/a", 3, day(9), "prio:P1"))
	f.AddIssue(ticket("o/a", 4, day(60)))
	h := stopped()

	runPass(t, f, h, "o/a")

	if !slices.Equal(h.seen, []int{3}) {
		t.Fatalf("tickets traités = %v, attendu [3] (P1 le plus ancien)", h.seen)
	}
}

func TestSkipsBlockedTickets(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	blocked := ticket("o/a", 1, day(10), "prio:P0")
	blocked.OpenBlockers = 1
	f.AddIssue(blocked)
	f.AddIssue(ticket("o/a", 2, day(1), "prio:P3"))
	h := stopped()

	runPass(t, f, h, "o/a")

	if !slices.Equal(h.seen, []int{2}) {
		t.Fatalf("tickets traités = %v, attendu [2] (le P0 est bloqué)", h.seen)
	}
}

func TestCapsTicketsPerPass(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.SetFile("o/b", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(3)))
	f.AddIssue(ticket("o/b", 1, day(2)))
	h := stopped()

	r := runPass(t, f, h, "o/a", "o/b")

	if len(h.seen) != 1 || len(r.Tickets) != 1 {
		t.Fatalf("tickets traités = %v, attendu un seul", h.seen)
	}
	if got := f.Issue("o/b", 1).Labels; !slices.Contains(got, forge.LabelReady) {
		t.Fatalf("le ticket non retenu a perdu ready-for-agent : %v", got)
	}
}

func TestIgnoresRepoWithoutValidOptIn(t *testing.T) {
	f := memforge.New()
	f.AddIssue(ticket("o/none", 1, day(9), "prio:P0"))
	f.SetFile("o/bad", optin.Path, "version: 7\n")
	f.AddIssue(ticket("o/bad", 1, day(9), "prio:P0"))
	h := stopped()

	r := runPass(t, f, h, "o/none", "o/bad")

	if len(h.seen) != 0 {
		t.Fatalf("tickets traités = %v, attendu aucun", h.seen)
	}
	statuses := map[string]pass.RepoStatus{}
	for _, rr := range r.Repos {
		statuses[rr.Repo] = rr.Status
	}
	if statuses["o/none"] != pass.NoOptIn || statuses["o/bad"] != pass.InvalidOptIn {
		t.Fatalf("statuts = %v", statuses)
	}
}

func TestEmptyQueueEndsCleanly(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	h := stopped()

	r := runPass(t, f, h, "o/a")

	if len(h.seen) != 0 || len(r.Tickets) != 0 {
		t.Fatalf("passe sur file vide : %+v", r)
	}
}

func TestStoppedTicketIsReservedThenHandedToHuman(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1), "prio:P2"))

	runPass(t, f, stopped(), "o/a")

	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelHuman) ||
		slices.Contains(i.Labels, forge.LabelReady) || slices.Contains(i.Labels, forge.LabelInProgress) {
		t.Fatalf("labels finaux = %v, attendu ready-for-human sans ready-for-agent ni agent-in-progress", i.Labels)
	}
	if len(i.Comments) != 2 {
		t.Fatalf("commentaires = %q, attendu réservation puis motif", i.Comments)
	}
	if !strings.Contains(i.Comments[0], "pass-test") || !strings.Contains(i.Comments[0], "2026-10-04") {
		t.Fatalf("commentaire de réservation = %q, attendu la passe et la date", i.Comments[0])
	}
	if !strings.Contains(i.Comments[1], "brief insuffisant") {
		t.Fatalf("commentaire final = %q, attendu le motif de l'arrêt", i.Comments[1])
	}
}

func TestHarnessErrorStillHandsTicketToHuman(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))

	r := runPass(t, f, &spyHarness{err: errors.New("conteneur tué")}, "o/a")

	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelHuman) || slices.Contains(i.Labels, forge.LabelInProgress) {
		t.Fatalf("labels finaux = %v", i.Labels)
	}
	if !strings.Contains(i.Comments[len(i.Comments)-1], "interrompu") {
		t.Fatalf("commentaire final = %q, attendu la mention de l'interruption", i.Comments[len(i.Comments)-1])
	}
	if r.Tickets[0].Outcome != harness.Failed {
		t.Fatalf("issue rapportée = %q, attendu failed", r.Tickets[0].Outcome)
	}
}

func TestSucceededTicketLeavesNoAgentLabel(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))

	runPass(t, f, &spyHarness{result: harness.Result{Outcome: harness.Succeeded}}, "o/a")

	i := f.Issue("o/a", 1)
	for _, l := range []string{forge.LabelReady, forge.LabelInProgress, forge.LabelHuman} {
		if slices.Contains(i.Labels, l) {
			t.Fatalf("labels finaux = %v, %s inattendu", i.Labels, l)
		}
	}
}
