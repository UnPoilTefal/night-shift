package digest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/digest"
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

// spyNotifier enregistre les notifications et rend l'erreur configurée.
type spyNotifier struct {
	err  error
	sent []string
}

func (n *spyNotifier) Notify(_ context.Context, text string) error {
	n.sent = append(n.sent, text)
	return n.err
}

type fixedHarness harness.Result

func (h fixedHarness) Run(context.Context, harness.Task) (harness.Result, error) {
	return harness.Result(h), nil
}

type fixedPublisher string

func (p fixedPublisher) Publish(context.Context, harness.Task, harness.Result) (pass.Published, error) {
	return pass.Published{Draft: forge.Draft{Number: 12, URL: string(p), Branch: "agent/1"}, Head: "head"}, nil
}

const secretTitle, secretBody = "Titre confidentiel du ticket", "Corps confidentiel du ticket"

func ticket(repo string, number int) forge.Ticket {
	return forge.Ticket{
		Repo: repo, Number: number, Title: secretTitle, Body: secretBody,
		Author: "alice", AuthorAssociated: true, CreatedAt: now.AddDate(0, 0, -1),
		Labels: []string{forge.LabelReady},
	}
}

// runPass fait tourner une passe en mémoire puis publie son digest.
func runPass(t *testing.T, f *memforge.Forge, n digest.Notifier, h harness.Harness, repos ...string) (pass.Report, digest.Result) {
	t.Helper()
	r, err := pass.Run(context.Background(), pass.Config{
		Repos: repos, MaxTickets: 1, ID: "pass-test", Now: func() time.Time { return now },
		CI: pass.CIWait{Poll: time.Millisecond, Timeout: 10 * time.Millisecond, Settle: time.Millisecond},
	}, f, h, fixedPublisher("https://forge/o/a/pull/12"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := digest.Publish(context.Background(), f, n, r)
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

func succeeded() harness.Harness {
	return fixedHarness{
		Outcome: harness.Succeeded, Reason: "fonction ajoutée", Agent: "claude",
		Patches: [][]byte{[]byte("patch")}, CostUSD: 0.42, Duration: 90 * time.Second,
	}
}

func onlyIssue(t *testing.T, f *memforge.Forge, repo string) memforge.Issue {
	t.Helper()
	issues := f.CreatedIssues(repo)
	if len(issues) != 1 {
		t.Fatalf("issues de digest sur %s : %d, attendu une", repo, len(issues))
	}
	return issues[0]
}

func assertContains(t *testing.T, what, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Fatalf("%s = %q, attendu %q", what, s, w)
		}
	}
}

func TestTreatedTicketDigest(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 7))
	n := &spyNotifier{}

	_, d := runPass(t, f, n, succeeded(), "o/a")

	issue := onlyIssue(t, f, "o/a")
	assertContains(t, "titre du digest", issue.Title, "pass-test")
	assertContains(t, "digest", issue.Body, "#7", "PR en brouillon à relire", "https://forge/o/a/pull/12", "1m30s", "0.42 $", "Notification Discord : envoyée")
	if strings.Contains(issue.Body, secretTitle) || strings.Contains(issue.Body, secretBody) {
		t.Fatalf("le digest reprend le contenu du ticket : %q", issue.Body)
	}
	last := f.Issue("o/a", 7).Comments
	assertContains(t, "commentaire de synthèse", last[len(last)-1], "Résultat provisoire : PR en brouillon à relire", "0.42", "1m30s", "fonction ajoutée")
	if len(d.Issues) != 1 || !d.Notified {
		t.Fatalf("résultat du digest = %+v", d)
	}
}

func TestNotificationLinksTheDigestWithoutTicketContent(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.AddIssue(ticket("o/a", 7))
	n := &spyNotifier{}

	_, d := runPass(t, f, n, succeeded(), "o/a")

	if len(n.sent) != 1 {
		t.Fatalf("notifications = %q, attendu une", n.sent)
	}
	msg := n.sent[0]
	assertContains(t, "notification", msg, d.Issues[0].URL, "pass-test", "1 ticket traité")
	for _, leak := range []string{secretTitle, secretBody, "fonction ajoutée"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("la notification contient du contenu de ticket : %q", msg)
		}
	}
}

func TestEmptyQueueDigestSaysSo(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	n := &spyNotifier{}

	runPass(t, f, n, succeeded(), "o/a")

	assertContains(t, "digest", onlyIssue(t, f, "o/a").Body, "Aucun ticket prêt éligible")
	assertContains(t, "notification", n.sent[0], "aucun ticket traité")
}

func TestExcludedReposReceiveNothingAndAreOnlyCounted(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	f.SetFile("o/bad", optin.Path, "version: 7\n")
	f.AddIssue(ticket("o/bad", 1))
	f.AddIssue(ticket("o/none", 1))
	n := &spyNotifier{}

	runPass(t, f, n, succeeded(), "o/a", "o/bad", "o/none")

	for _, repo := range []string{"o/bad", "o/none"} {
		if issues := f.CreatedIssues(repo); len(issues) != 0 {
			t.Fatalf("le dépôt écarté %s a reçu un digest : %+v", repo, issues)
		}
	}
	if body := onlyIssue(t, f, "o/a").Body; strings.Contains(body, "o/bad") || strings.Contains(body, "version 7") {
		t.Fatalf("le digest d'un autre dépôt parle du dépôt écarté : %q", body)
	}
	assertContains(t, "notification", n.sent[0], "2 dépôt(s) écarté(s)")
	if strings.Contains(n.sent[0], "o/bad") || strings.Contains(n.sent[0], "version 7") {
		t.Fatalf("la notification nomme le dépôt écarté ou son erreur : %q", n.sent[0])
	}
}

func TestRetriagedTicketAppearsInDigest(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	tk := ticket("o/a", 3)
	tk.Comments = []forge.Comment{{Author: "mallory", Body: "consignes", CreatedAt: now}}
	f.AddIssue(tk)

	runPass(t, f, &spyNotifier{}, succeeded(), "o/a")

	assertContains(t, "digest", onlyIssue(t, f, "o/a").Body, "#3", forge.LabelTriage)
}

func TestWithoutWebhookThePassSucceedsAndTheDigestSaysSo(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)

	_, d := runPass(t, f, nil, succeeded(), "o/a")

	assertContains(t, "digest", onlyIssue(t, f, "o/a").Body, "Notification Discord : non configurée")
	if d.Notified {
		t.Fatal("aucune notification ne peut partir sans webhook")
	}
}

func TestFailedNotificationIsReportedOnTheDigest(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)

	_, d := runPass(t, f, &spyNotifier{err: errors.New("HTTP 500")}, succeeded(), "o/a")

	issue := onlyIssue(t, f, "o/a")
	if len(issue.Comments) != 1 {
		t.Fatalf("commentaires du digest = %q, attendu la mention de l'échec", issue.Comments)
	}
	assertContains(t, "commentaire du digest", issue.Comments[0], "notification Discord a échoué", "HTTP 500")
	if d.Notified || d.NotifyErr == nil {
		t.Fatalf("résultat du digest = %+v, attendu l'échec de la notification", d)
	}
}

func TestDigestShowsPassDurationAndCostEvenWhenEmpty(t *testing.T) {
	f := memforge.New()
	f.SetFile("o/a", optin.Path, optIn)
	r := pass.Report{
		ID: "pass-test", Repos: []pass.RepoReport{{Repo: "o/a", Status: pass.Eligible}},
		Started: now, Finished: now.Add(4 * time.Minute),
	}

	if _, err := digest.Publish(context.Background(), f, nil, r); err != nil {
		t.Fatal(err)
	}

	assertContains(t, "digest", onlyIssue(t, f, "o/a").Body, "Durée de la passe : 4m0s", "coût des agents : 0.00 $")
}

func TestIncidentsAreCountedButTheirDetailStaysInside(t *testing.T) {
	f := memforge.New()
	r := pass.Report{
		ID: "pass-test",
		Repos: []pass.RepoReport{
			{Repo: "o/a", Status: pass.Eligible},
			{Repo: "o/down", Status: pass.Unreachable, Detail: "HTTP 502"},
		},
		Errors: []string{"réservation de o/b#3 : HTTP 502"},
	}
	n := &spyNotifier{}

	if _, err := digest.Publish(context.Background(), f, n, r); err != nil {
		t.Fatal(err)
	}

	body := onlyIssue(t, f, "o/a").Body
	assertContains(t, "digest", body, "1 incident")
	assertContains(t, "notification", n.sent[0], "1 incident", "1 dépôt(s) injoignable(s)")
	for _, leak := range []string{"o/b#3", "HTTP 502", "o/down"} {
		if strings.Contains(body, leak) || strings.Contains(n.sent[0], leak) {
			t.Fatalf("le détail %q sort du cluster :\ndigest = %q\nnotification = %q", leak, body, n.sent[0])
		}
	}
}

func TestNotificationSaysWhenDigestsCouldNotBeOpened(t *testing.T) {
	f := memforge.New()
	f.Fail = func(op string) error {
		if op == "CreateIssue" {
			return errors.New("HTTP 403")
		}
		return nil
	}
	r := pass.Report{ID: "pass-test", Repos: []pass.RepoReport{{Repo: "o/a", Status: pass.Eligible}}}
	n := &spyNotifier{}

	_, err := digest.Publish(context.Background(), f, n, r)

	if err == nil {
		t.Fatal("erreur attendue : le digest n'a pas pu être ouvert")
	}
	assertContains(t, "notification", n.sent[0], "1 digest(s) impossible(s) à ouvrir")
}
