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

func (h *spyHarness) Run(_ context.Context, t harness.Task) (harness.Result, error) {
	h.seen = append(h.seen, t.Ticket.Number)
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
	}, f, h, nil)
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

// spyPublisher enregistre ce qu'on lui confie et rend l'URL ou l'erreur
// configurée.
type spyPublisher struct {
	url   string
	err   error
	tasks []harness.Task
	res   []harness.Result
}

func (p *spyPublisher) Publish(_ context.Context, t harness.Task, res harness.Result) (string, error) {
	p.tasks = append(p.tasks, t)
	p.res = append(p.res, res)
	return p.url, p.err
}

func succeeded() *spyHarness {
	return &spyHarness{result: harness.Result{
		Outcome: harness.Succeeded, Reason: "fonction ajoutée", Agent: "claude",
		Patches: [][]byte{[]byte("patch 1"), []byte("patch 2")}, CostUSD: 0.42, Duration: 90 * time.Second, SessionID: "s-1",
	}}
}

func runWith(t *testing.T, f *memforge.Forge, h harness.Harness, p pass.Publisher) pass.Report {
	t.Helper()
	r, err := pass.Run(context.Background(), pass.Config{
		Repos: []string{"o/a"}, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
	}, f, h, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertNoLabel(t *testing.T, labels []string, unwanted ...string) {
	t.Helper()
	for _, l := range unwanted {
		if slices.Contains(labels, l) {
			t.Fatalf("labels finaux = %v, %s inattendu", labels, l)
		}
	}
}

func TestSucceededTicketIsPublishedAsDraftAndLeavesNoAgentLabel(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	tk := ticket("o/a", 7, day(1))
	tk.Title, tk.Body = "Ajouter la fonction", "Le brief qui fait foi."
	f.AddIssue(tk)
	p := &spyPublisher{url: "https://forge/o/a/pull/12"}

	r := runWith(t, f, succeeded(), p)

	if len(p.tasks) != 1 || len(p.res[0].Patches) != 2 {
		t.Fatalf("Publication appelée %d fois, attendu une fois avec les deux patchs", len(p.tasks))
	}
	if got := p.tasks[0]; got.PassID != "pass-test" || !strings.Contains(got.Brief, "Le brief qui fait foi.") {
		t.Fatalf("tâche publiée = %+v", got)
	}
	i := f.Issue("o/a", 7)
	assertNoLabel(t, i.Labels, forge.LabelReady, forge.LabelInProgress, forge.LabelHuman, forge.LabelNeedsInfo)
	last := i.Comments[len(i.Comments)-1]
	for _, want := range []string{"https://forge/o/a/pull/12", "0.42", "1m30s"} {
		if !strings.Contains(last, want) {
			t.Fatalf("commentaire final = %q, attendu %q", last, want)
		}
	}
	if tr := r.Tickets[0]; tr.Outcome != harness.Succeeded || tr.PullRequest != "https://forge/o/a/pull/12" {
		t.Fatalf("rapport = %+v", tr)
	}
}

func TestNeedsInfoTicketGoesBackToNeedsInfo(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))
	h := &spyHarness{result: harness.Result{Outcome: harness.NeedsInfo, Reason: "la dépendance foo n'est pas nommée"}}
	p := &spyPublisher{}

	runWith(t, f, h, p)

	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelNeedsInfo) {
		t.Fatalf("labels finaux = %v, attendu needs-info", i.Labels)
	}
	assertNoLabel(t, i.Labels, forge.LabelReady, forge.LabelInProgress, forge.LabelHuman)
	if !strings.Contains(i.Comments[len(i.Comments)-1], "la dépendance foo n'est pas nommée") {
		t.Fatalf("commentaire final = %q, attendu le motif", i.Comments[len(i.Comments)-1])
	}
	if len(p.tasks) != 0 {
		t.Fatal("rien ne doit être publié sur un arrêt needs-info")
	}
}

func TestFailedAgentHandsTicketToHumanWithoutPublishing(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))
	h := &spyHarness{result: harness.Result{Outcome: harness.Failed, Reason: "durée maximale atteinte (25m0s)"}}
	p := &spyPublisher{}

	runWith(t, f, h, p)

	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelHuman) {
		t.Fatalf("labels finaux = %v, attendu ready-for-human", i.Labels)
	}
	assertNoLabel(t, i.Labels, forge.LabelInProgress, forge.LabelNeedsInfo)
	last := i.Comments[len(i.Comments)-1]
	if !strings.Contains(last, "interrompu") || !strings.Contains(last, "durée maximale") {
		t.Fatalf("commentaire final = %q, attendu la mention de l'interruption", last)
	}
	if len(p.tasks) != 0 {
		t.Fatal("rien ne doit être publié sur un échec")
	}
}

func TestFailedPublicationHandsTicketToHuman(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))

	r := runWith(t, f, succeeded(), &spyPublisher{err: errors.New("git am : conflit")})

	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelHuman) || slices.Contains(i.Labels, forge.LabelInProgress) {
		t.Fatalf("labels finaux = %v, attendu ready-for-human", i.Labels)
	}
	if last := i.Comments[len(i.Comments)-1]; !strings.Contains(last, "git am : conflit") || !strings.Contains(last, "0.42") {
		t.Fatalf("commentaire final = %q, attendu la cause et le coût de l'agent", last)
	}
	if r.Tickets[0].Outcome != harness.Failed {
		t.Fatalf("issue rapportée = %q, attendu failed", r.Tickets[0].Outcome)
	}
}

func TestSuccessWithoutCommitsOrPublisherHandsTicketToHuman(t *testing.T) {
	for name, tc := range map[string]struct {
		h harness.Harness
		p pass.Publisher
	}{
		"sans commit":      {&spyHarness{result: harness.Result{Outcome: harness.Succeeded}}, &spyPublisher{}},
		"sans Publication": {succeeded(), nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := memforge.New()
			f.SetFile("o/a", optin.Path, optIn)
			f.AddIssue(ticket("o/a", 1, day(1)))

			runWith(t, f, tc.h, tc.p)

			if i := f.Issue("o/a", 1); !slices.Contains(i.Labels, forge.LabelHuman) {
				t.Fatalf("labels finaux = %v, attendu ready-for-human", i.Labels)
			}
		})
	}
}

func TestFailedReservationPutsTicketBackInQueue(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))
	f.Fail = func(op string) error {
		if op == "Comment" {
			return errors.New("HTTP 502")
		}
		return nil
	}
	h := stopped()

	_, err := pass.Run(context.Background(), pass.Config{
		Repos: []string{"o/a"}, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
	}, f, h, nil)

	if err == nil {
		t.Fatal("erreur attendue")
	}
	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelReady) || slices.Contains(i.Labels, forge.LabelInProgress) {
		t.Fatalf("labels = %v, attendu le ticket remis en file", i.Labels)
	}
	if len(h.seen) != 0 {
		t.Fatal("le harness ne doit pas tourner sur une réservation ratée")
	}
}

// cancellingHarness annule le contexte de la passe, comme un arrêt par
// signal ou un dépassement de durée pendant le travail de l'agent.
type cancellingHarness struct{ cancel context.CancelFunc }

func (h cancellingHarness) Run(ctx context.Context, _ harness.Task) (harness.Result, error) {
	h.cancel()
	return harness.Result{}, ctx.Err()
}

func TestCancelledPassStillHandsTicketBack(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 1, day(1)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := pass.Run(ctx, pass.Config{
		Repos: []string{"o/a"}, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
	}, f, cancellingHarness{cancel}, nil)

	if err != nil {
		t.Fatal(err)
	}
	i := f.Issue("o/a", 1)
	if !slices.Contains(i.Labels, forge.LabelHuman) || slices.Contains(i.Labels, forge.LabelInProgress) {
		t.Fatalf("labels = %v, attendu ready-for-human malgré l'annulation", i.Labels)
	}
}

func TestDuplicateReposAreProcessedOnce(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, strings.Replace(optIn, "ticketsPerPass: 1", "ticketsPerPass: 5", 1))
	f.AddIssue(ticket("o/a", 1, day(1)))
	h := stopped()

	r, err := pass.Run(context.Background(), pass.Config{
		Repos: []string{"o/a", "O/A", "o/a"}, MaxTickets: 5, ID: "pass-test", Now: func() time.Time { return now },
	}, f, h, nil)

	if err != nil {
		t.Fatal(err)
	}
	if len(h.seen) != 1 || len(r.Repos) != 1 {
		t.Fatalf("tickets traités = %v, dépôts = %+v, attendu un seul passage", h.seen, r.Repos)
	}
}
