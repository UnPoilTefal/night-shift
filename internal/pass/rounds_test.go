package pass_test

import (
	"context"
	"fmt"
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

// roundsHarness rend un résultat par tour, dans l'ordre, et enregistre les
// tâches reçues.
type roundsHarness struct {
	results []harness.Result
	tasks   []harness.Task
}

func (h *roundsHarness) Run(_ context.Context, t harness.Task) (harness.Result, error) {
	h.tasks = append(h.tasks, t)
	if len(h.tasks) > len(h.results) {
		return harness.Result{Outcome: harness.Failed, Reason: "tour inattendu"}, nil
	}
	return h.results[len(h.tasks)-1], nil
}

func commits(cost float64) harness.Result {
	return harness.Result{Outcome: harness.Succeeded, Reason: "fait", Agent: "claude", Patches: [][]byte{[]byte("p")}, CostUSD: cost, Duration: time.Minute}
}

// forgePublisher ouvre la PR sur la forge en mémoire au premier tour, puis
// pousse sur la même branche ; la tête publiée au tour n est head-n.
type forgePublisher struct {
	f     *memforge.Forge
	tasks []harness.Task
}

func (p *forgePublisher) Publish(ctx context.Context, t harness.Task, _ harness.Result) (pass.Published, error) {
	p.tasks = append(p.tasks, t)
	head := fmt.Sprintf("head-%d", len(p.tasks))
	if t.PullRequest != nil {
		return pass.Published{Draft: *t.PullRequest, Head: head}, nil
	}
	n, url, err := p.f.OpenDraftPR(ctx, t.Ticket.Repo, forge.PullRequest{Head: "agent/7", Base: "main", Title: t.Ticket.Title})
	return pass.Published{Draft: forge.Draft{Number: n, URL: url, Branch: "agent/7"}, Head: head}, err
}

var (
	green = forge.Check{Name: "test", State: forge.CheckPassed}
	red   = forge.Check{Name: "test", State: forge.CheckFailed, Excerpt: "--- FAIL: TestF", URL: "https://ci/1"}
	lint  = forge.Check{Name: "lint", State: forge.CheckPassed}
)

func ciForge() *memforge.Forge {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 7, day(1)))
	return f
}

func runRounds(t *testing.T, f *memforge.Forge, h harness.Harness, rounds int) (pass.Report, *forgePublisher) {
	t.Helper()
	p := &forgePublisher{f: f}
	r, err := pass.Run(context.Background(), pass.Config{
		Repos: []string{"o/a"}, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
		CI: pass.CIWait{Poll: time.Millisecond, Timeout: 50 * time.Millisecond, Settle: 5 * time.Millisecond}, Rounds: rounds,
	}, f, h, p)
	if err != nil {
		t.Fatal(err)
	}
	return r, p
}

func lastComment(i memforge.Issue) string { return i.Comments[len(i.Comments)-1] }

func TestGreenCIOnFirstRoundEndsWithTheDraft(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", green, lint)
	h := &roundsHarness{results: []harness.Result{commits(0.5)}}

	r, _ := runRounds(t, f, h, 0)

	if len(h.tasks) != 1 {
		t.Fatalf("l'agent a tourné %d fois, attendu une seule", len(h.tasks))
	}
	if h.tasks[0].Round != 1 {
		t.Fatalf("premier tour numéroté %d", h.tasks[0].Round)
	}
	i := f.Issue("o/a", 7)
	assertNoLabel(t, i.Labels, forge.LabelReady, forge.LabelInProgress, forge.LabelHuman)
	if c := lastComment(i); !strings.Contains(c, "memforge://o/a/pull/8") || !strings.Contains(c, "CI verte") {
		t.Fatalf("commentaire final = %q", c)
	}
	if tr := r.Tickets[0]; tr.Outcome != harness.Succeeded || tr.PullRequest != "memforge://o/a/pull/8" {
		t.Fatalf("rapport = %+v", tr)
	}
}

func TestRedCIRelaunchesTheAgentOnTheSameBranch(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red, lint)
	f.SetChecks("o/a", "head-2", green, lint)
	h := &roundsHarness{results: []harness.Result{commits(0.5), commits(0.25)}}

	r, p := runRounds(t, f, h, 0)

	if len(h.tasks) != 2 {
		t.Fatalf("l'agent a tourné %d fois, attendu deux", len(h.tasks))
	}
	retry := h.tasks[1]
	if retry.Round != 2 || retry.BaseSHA != "head-1" || retry.PullRequest == nil || retry.PullRequest.Number != 8 {
		t.Fatalf("tâche du deuxième tour = %+v, attendu le tour 2 sur la tête publiée de la PR #8", retry)
	}
	if len(retry.CIFailures) != 1 || retry.CIFailures[0].Name != "test" || retry.CIFailures[0].Excerpt != "--- FAIL: TestF" {
		t.Fatalf("checks transmis à l'agent = %+v, attendu le seul check en échec avec son extrait", retry.CIFailures)
	}
	if retry.Brief != h.tasks[0].Brief {
		t.Fatal("le brief a changé entre les tours")
	}
	if len(f.DraftPRs("o/a")) != 1 || p.tasks[1].PullRequest == nil {
		t.Fatalf("PR ouvertes = %d, attendu une seule, poussée deux fois", len(f.DraftPRs("o/a")))
	}
	i := f.Issue("o/a", 7)
	assertNoLabel(t, i.Labels, forge.LabelInProgress, forge.LabelHuman)
	if c := lastComment(i); !strings.Contains(c, "0.75") || !strings.Contains(c, "2 tours") {
		t.Fatalf("commentaire final = %q, attendu le coût cumulé et le nombre de tours", c)
	}
	if tr := r.Tickets[0]; tr.Outcome != harness.Succeeded || tr.CostUSD != 0.75 || tr.Duration != 2*time.Minute {
		t.Fatalf("rapport = %+v, attendu un succès au coût et à la durée cumulés", tr)
	}
}

func TestRedCIAfterTwoRoundsLeavesTheDraftToAHuman(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red)
	f.SetChecks("o/a", "head-2", red, lint)
	h := &roundsHarness{results: []harness.Result{commits(0.5), commits(0.25), commits(0)}}

	r, _ := runRounds(t, f, h, 0)

	if len(h.tasks) != 2 {
		t.Fatalf("l'agent a tourné %d fois : un troisième tour n'est jamais tenté", len(h.tasks))
	}
	pr := f.Pull("o/a", 8)
	if len(pr.Comments) != 1 || !strings.Contains(pr.Comments[0], "test") || !strings.Contains(pr.Comments[0], "--- FAIL: TestF") || strings.Contains(pr.Comments[0], "lint") {
		t.Fatalf("commentaires de la PR = %q, attendu un résumé du seul check en échec", pr.Comments)
	}
	i := f.Issue("o/a", 7)
	if !slices.Contains(i.Labels, forge.LabelHuman) {
		t.Fatalf("labels finaux = %v, attendu %s", i.Labels, forge.LabelHuman)
	}
	assertNoLabel(t, i.Labels, forge.LabelInProgress, forge.LabelReady)
	if c := lastComment(i); !strings.Contains(c, "memforge://o/a/pull/8") || !strings.Contains(c, "brouillon") {
		t.Fatalf("commentaire final = %q", c)
	}
	if tr := r.Tickets[0]; tr.Outcome != pass.CIFailed || tr.PullRequest != "memforge://o/a/pull/8" {
		t.Fatalf("rapport = %+v", tr)
	}
}

func TestSingleRoundPassNeverRelaunchesTheAgent(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red)
	h := &roundsHarness{results: []harness.Result{commits(0.5), commits(0.25)}}

	r, _ := runRounds(t, f, h, 1)

	if len(h.tasks) != 1 || r.Tickets[0].Outcome != pass.CIFailed {
		t.Fatalf("tours = %d, issue = %s ; attendu un seul tour, CI rouge", len(h.tasks), r.Tickets[0].Outcome)
	}
}

func TestUnsettledCIAtDeadlineLeavesTheDraftForReview(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", green, forge.Check{Name: "e2e", State: forge.CheckPending})
	h := &roundsHarness{results: []harness.Result{commits(0.5)}}

	r, _ := runRounds(t, f, h, 0)

	if len(h.tasks) != 1 || r.Tickets[0].Outcome != harness.Succeeded {
		t.Fatalf("tours = %d, issue = %s ; attendu la PR à relire sans relance", len(h.tasks), r.Tickets[0].Outcome)
	}
	if c := lastComment(f.Issue("o/a", 7)); !strings.Contains(c, "CI non conclue") || !strings.Contains(c, "e2e") {
		t.Fatalf("commentaire final = %q, attendu la CI non conclue et le check en cours", c)
	}
}

func TestFailingCheckAtDeadlineCountsAsRed(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red, forge.Check{Name: "e2e", State: forge.CheckPending})
	f.SetChecks("o/a", "head-2", green)
	h := &roundsHarness{results: []harness.Result{commits(0.5), commits(0.25)}}

	runRounds(t, f, h, 0)

	if len(h.tasks) != 2 {
		t.Fatalf("l'agent a tourné %d fois, attendu une relance malgré un check encore en cours", len(h.tasks))
	}
}

func TestAgentStoppingOnSecondRoundHandsTicketBackWithTheDraft(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red)
	h := &roundsHarness{results: []harness.Result{commits(0.5), {Outcome: harness.Stopped, Reason: "test instable"}}}

	r, _ := runRounds(t, f, h, 0)

	i := f.Issue("o/a", 7)
	if !slices.Contains(i.Labels, forge.LabelHuman) {
		t.Fatalf("labels finaux = %v", i.Labels)
	}
	if c := lastComment(i); !strings.Contains(c, "test instable") || !strings.Contains(c, "memforge://o/a/pull/8") {
		t.Fatalf("commentaire final = %q, attendu le motif et la PR en brouillon", c)
	}
	if tr := r.Tickets[0]; tr.PullRequest != "memforge://o/a/pull/8" || tr.CostUSD != 0.5 {
		t.Fatalf("rapport = %+v", tr)
	}
}

func TestLateCheckKeepsCIFromSettlingTooEarly(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", green)
	f.SetChecks("o/a", "head-2", green)
	// Un workflow lent ne s'enregistre qu'après la première lecture, quand
	// le check rapide est déjà vert.
	reads := 0
	f.Fail = func(op string) error {
		if op == "Checks" {
			if reads++; reads == 2 {
				f.SetChecks("o/a", "head-1", green, red)
			}
		}
		return nil
	}
	h := &roundsHarness{results: []harness.Result{commits(0.5), commits(0.25)}}

	runRounds(t, f, h, 0)

	if len(h.tasks) != 2 {
		t.Fatalf("l'agent a tourné %d fois : la CI a été jugée avant l'arrivée du check lent", len(h.tasks))
	}
}

func TestRetryKeepsTheRoundToSettleIfItCannotHappen(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red)
	_, tasks, err := pass.Select(context.Background(), pass.Config{Repos: []string{"o/a"}, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now }}, f)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("sélection : %v, %d tâche(s)", err, len(tasks))
	}
	res := commits(0.5)
	res.Transcript, res.SessionID = "/transcripts/s-1.jsonl", "s-1"

	fu := pass.Follow(context.Background(), pass.Config{ID: "pass-test", CI: pass.CIWait{Poll: time.Millisecond, Timeout: 50 * time.Millisecond, Settle: time.Millisecond}},
		f, &forgePublisher{f: f}, tasks[0], res)

	if !fu.Retry || fu.Next.Round != 2 {
		t.Fatalf("suite = %+v, attendu une relance au tour 2", fu)
	}
	// Si la relance ne peut pas être préparée, ce tour se rend tel quel.
	if fu.Result.Outcome != pass.CIFailed || fu.Result.Transcript != res.Transcript || fu.Task.Round != 1 || fu.Task.PullRequest == nil {
		t.Fatalf("rendu de repli = %+v, tâche = %+v", fu.Result, fu.Task)
	}
	tr, err := pass.Settle(context.Background(), pass.Config{ID: "pass-test"}, f, fu.Task, fu.Result)
	if err != nil || tr.CostUSD != 0.5 || tr.Transcript != res.Transcript {
		t.Fatalf("rapport = %+v, %v ; attendu le coût et le transcript du seul tour", tr, err)
	}
}

func TestUnfinishedRetryStillSumsUpTheRedCIOnThePR(t *testing.T) {
	f := ciForge()
	f.SetChecks("o/a", "head-1", red)
	h := &roundsHarness{results: []harness.Result{commits(0.5), {Outcome: harness.Stopped, Reason: "test instable"}}}

	runRounds(t, f, h, 0)

	pr := f.Pull("o/a", 8)
	if len(pr.Comments) != 1 || !strings.Contains(pr.Comments[0], "--- FAIL: TestF") || !strings.Contains(pr.Comments[0], "test instable") {
		t.Fatalf("commentaires de la PR = %q, attendu les checks en échec et le motif de la relance inaboutie", pr.Comments)
	}
}
