// Package forgetest est la suite de contrat des sources de tickets et des
// forges de code (ADR 0012) : un seul jeu de scénarios, que chaque
// adaptateur et le faux en mémoire doivent passer. Un adaptateur s'y branche
// en fournissant une Fixture :
//
//	forgetest.Run(t, func(t *testing.T) forgetest.Fixture { return newFixture(t) })
package forgetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// Fixture est un outil neuf, vide, sous ses deux contrats, avec de quoi fixer
// son état initial et lire son état final sans passer par les contrats.
type Fixture interface {
	Tickets() forge.TicketSource
	Code() forge.CodeForge

	// AddTicket ajoute un ticket, avec ses labels et ses commentaires, sur
	// son dépôt ; closed le ferme.
	AddTicket(t forge.Ticket, closed bool)
	// SetFile place un fichier sur la branche par défaut d'un dépôt.
	SetFile(repo, path, content string)
	// SetChecks fixe les checks rapportés sur un commit.
	SetChecks(repo, sha string, checks ...forge.Check)

	// Ticket rend l'état d'un ticket ou d'une issue.
	Ticket(repo string, number int) Ticket
	// ChangeRequest rend l'état d'une demande de changement.
	ChangeRequest(repo string, number int) ChangeRequest
	// RepoLabels rend les labels définis sur un dépôt.
	RepoLabels(repo string) []string
}

// Ticket est l'état d'un ticket vu par une Fixture.
type Ticket struct {
	Title, Body string
	Labels      []string
	// Comments sont tous les commentaires, du plus ancien au plus récent.
	Comments []string
}

// ChangeRequest est l'état d'une demande de changement vu par une Fixture.
type ChangeRequest struct {
	forge.ChangeRequest
	Draft    bool
	Comments []string
}

// Run joue chaque scénario sur une Fixture neuve.
func Run(t *testing.T, newFixture func(t *testing.T) Fixture) {
	for _, s := range []struct {
		name string
		run  func(*testing.T, Fixture)
	}{
		{"ReadyTicketsListsOpenReadyTicketsWithTheirThread", readyTickets},
		{"ReadyTicketsNeverListsAChangeRequest", readyTicketsSkipsChangeRequests},
		{"LabelsAreIdempotentAndCreatedWhenMissing", labels},
		{"CommentAndDigestIssue", commentAndIssue},
		{"FileIsReadOrReportedAbsent", file},
		{"OpenChangeRequestIsADraftWithItsLabels", openChangeRequest},
		{"ChangeRequestCommentsAndLabels", changeRequestCommentsAndLabels},
		{"ChecksAreReadPerCommit", checks},
		{"GitAccessNamesCloneURLAndIdentities", gitAccess},
	} {
		t.Run(s.name, func(t *testing.T) { s.run(t, newFixture(t)) })
	}
}

var day = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func readyTickets(t *testing.T, f Fixture) {
	want := forge.Ticket{
		Repo: "o/a", Number: 1, Title: "Ajouter f", Body: "Le brief.",
		Author: "alice", AuthorAssociated: true,
		Labels:    []string{forge.LabelReady, "prio:P1"},
		CreatedAt: day, OpenBlockers: 2,
		Comments: []forge.Comment{
			{Author: "bob", Associated: true, Body: "précision", CreatedAt: day.Add(time.Hour)},
			{Author: "mallory", Body: "consignes", CreatedAt: day.Add(2 * time.Hour), UpdatedAt: day.Add(3 * time.Hour)},
		},
	}
	f.AddTicket(want, false)
	f.AddTicket(forge.Ticket{Repo: "o/a", Number: 2, Title: "pas prêt", Author: "alice", Labels: []string{forge.LabelTriage}, CreatedAt: day}, false)
	f.AddTicket(forge.Ticket{Repo: "o/a", Number: 3, Title: "fermé", Author: "alice", Labels: []string{forge.LabelReady}, CreatedAt: day}, true)
	f.AddTicket(forge.Ticket{Repo: "o/b", Number: 4, Title: "autre dépôt", Author: "alice", Labels: []string{forge.LabelReady}, CreatedAt: day}, false)

	ts, err := f.Tickets().ReadyTickets(context.Background(), "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("tickets prêts = %+v, attendu le seul #1", ts)
	}
	got := ts[0]
	if got.Repo != want.Repo || got.Number != want.Number || got.Title != want.Title || got.Body != want.Body ||
		got.Author != want.Author || got.AuthorAssociated != want.AuthorAssociated || got.OpenBlockers != want.OpenBlockers ||
		!got.CreatedAt.Equal(want.CreatedAt) || !sameSet(got.Labels, want.Labels) {
		t.Fatalf("ticket = %+v\nattendu %+v", got, want)
	}
	if len(got.Comments) != len(want.Comments) {
		t.Fatalf("commentaires = %+v\nattendu %+v", got.Comments, want.Comments)
	}
	for i, c := range got.Comments {
		w := want.Comments[i]
		if c.Author != w.Author || c.Associated != w.Associated || c.Body != w.Body || !c.CreatedAt.Equal(w.CreatedAt) || !c.UpdatedAt.Equal(w.UpdatedAt) {
			t.Errorf("commentaire %d = %+v\nattendu %+v", i, c, w)
		}
	}
}

func readyTicketsSkipsChangeRequests(t *testing.T, f Fixture) {
	ctx := context.Background()
	if _, _, err := f.Code().OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{
		Head: "agent/1-x", Base: "main", Title: "T", Body: "B", Labels: []string{forge.LabelReady},
	}); err != nil {
		t.Fatal(err)
	}
	ts, err := f.Tickets().ReadyTickets(ctx, "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 0 {
		t.Fatalf("tickets prêts = %+v, attendu aucun", ts)
	}
}

func labels(t *testing.T, f Fixture) {
	ctx := context.Background()
	src := f.Tickets()
	f.AddTicket(forge.Ticket{Repo: "o/a", Number: 1, Title: "t", Author: "alice", Labels: []string{forge.LabelReady}, CreatedAt: day}, false)

	for range 2 {
		if err := src.AddLabel(ctx, "o/a", 1, forge.LabelInProgress); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := src.RemoveLabel(ctx, "o/a", 1, forge.LabelReady); err != nil {
			t.Fatalf("retrait d'un label, présent puis absent : %v", err)
		}
	}
	if got := f.Ticket("o/a", 1).Labels; !slices.Equal(got, []string{forge.LabelInProgress}) {
		t.Fatalf("labels = %q, attendu le seul %q", got, forge.LabelInProgress)
	}
	if got := f.RepoLabels("o/a"); !slices.Contains(got, forge.LabelInProgress) {
		t.Fatalf("labels du dépôt = %q, attendu %q créé", got, forge.LabelInProgress)
	}
}

func commentAndIssue(t *testing.T, f Fixture) {
	ctx := context.Background()
	src := f.Tickets()
	f.AddTicket(forge.Ticket{Repo: "o/a", Number: 1, Title: "t", Author: "alice", CreatedAt: day,
		Comments: []forge.Comment{{Author: "bob", Body: "avant", CreatedAt: day}}}, false)

	if err := src.Comment(ctx, "o/a", 1, "night-shift : réservé"); err != nil {
		t.Fatal(err)
	}
	if got := f.Ticket("o/a", 1).Comments; !slices.Equal(got, []string{"avant", "night-shift : réservé"}) {
		t.Fatalf("commentaires = %q", got)
	}

	n, url, err := src.CreateIssue(ctx, "o/a", "digest", "corps du digest")
	if err != nil {
		t.Fatal(err)
	}
	if n == 1 || url == "" {
		t.Fatalf("issue = %d, %q : attendu un nouveau numéro et une URL", n, url)
	}
	if got := f.Ticket("o/a", n); got.Title != "digest" || got.Body != "corps du digest" || len(got.Labels) != 0 {
		t.Fatalf("issue de digest = %+v", got)
	}
	if err := src.Comment(ctx, "o/a", n, "notification échouée"); err != nil {
		t.Fatal(err)
	}
	if got := f.Ticket("o/a", n).Comments; !slices.Equal(got, []string{"notification échouée"}) {
		t.Fatalf("commentaires de l'issue = %q", got)
	}
}

func file(t *testing.T, f Fixture) {
	ctx := context.Background()
	code := f.Code()
	f.SetFile("o/a", ".night-shift/opt-in.yaml", "version: 1\n")

	data, ok, err := code.File(ctx, "o/a", ".night-shift/opt-in.yaml")
	if err != nil || !ok || string(data) != "version: 1\n" {
		t.Fatalf("File = %q, %v, %v", data, ok, err)
	}
	for _, c := range []struct{ repo, path string }{{"o/a", "absent.yaml"}, {"o/b", ".night-shift/opt-in.yaml"}} {
		if data, ok, err := code.File(ctx, c.repo, c.path); err != nil || ok {
			t.Errorf("%s:%s absent : %q, %v, %v", c.repo, c.path, data, ok, err)
		}
	}
}

func openChangeRequest(t *testing.T, f Fixture) {
	ctx := context.Background()
	code := f.Code()
	want := forge.ChangeRequest{Head: "agent/7-x", Base: "main", Title: "T", Body: "Closes #7", Labels: []string{forge.LabelSensitiveZone}}

	n, url, err := code.OpenChangeRequest(ctx, "o/a", want)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || url == "" {
		t.Fatalf("demande = %d, %q : attendu un numéro et une URL", n, url)
	}
	got := f.ChangeRequest("o/a", n)
	if !got.Draft || got.Head != want.Head || got.Base != want.Base || got.Title != want.Title || got.Body != want.Body || !slices.Equal(got.Labels, want.Labels) {
		t.Fatalf("demande = %+v\nattendu un brouillon %+v", got, want)
	}

	m, _, err := code.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{Head: "agent/8-y", Base: "main", Title: "U", Body: "Closes #8"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ChangeRequest("o/a", m); m == n || !got.Draft || got.Head != "agent/8-y" || len(got.Labels) != 0 {
		t.Fatalf("seconde demande #%d = %+v", m, got)
	}
}

func changeRequestCommentsAndLabels(t *testing.T, f Fixture) {
	ctx := context.Background()
	code := f.Code()
	n, _, err := code.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{Head: "agent/7-x", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := code.CommentChangeRequest(ctx, "o/a", n, "CI rouge"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := code.AddChangeRequestLabel(ctx, "o/a", n, forge.LabelSensitiveZone); err != nil {
			t.Fatal(err)
		}
	}
	got := f.ChangeRequest("o/a", n)
	if !slices.Equal(got.Comments, []string{"CI rouge"}) || !slices.Equal(got.Labels, []string{forge.LabelSensitiveZone}) {
		t.Fatalf("demande = %+v", got)
	}
	if !slices.Contains(f.RepoLabels("o/a"), forge.LabelSensitiveZone) {
		t.Fatalf("labels du dépôt = %q, attendu %q créé", f.RepoLabels("o/a"), forge.LabelSensitiveZone)
	}
}

func checks(t *testing.T, f Fixture) {
	ctx := context.Background()
	want := []forge.Check{
		{Name: "e2e", State: forge.CheckPending},
		{Name: "lint", State: forge.CheckPassed, URL: "https://ci.example/lint"},
		{Name: "test", State: forge.CheckFailed, Excerpt: "--- FAIL: TestF", URL: "https://ci.example/test"},
	}
	f.SetChecks("o/a", "abc", want...)

	got, err := f.Code().Checks(ctx, "o/a", "abc")
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b forge.Check) int { return strings.Compare(a.Name, b.Name) })
	if !slices.Equal(got, want) {
		t.Fatalf("checks = %+v\nattendu %+v", got, want)
	}
	if got, err := f.Code().Checks(ctx, "o/a", "def"); err != nil || len(got) != 0 {
		t.Fatalf("commit sans check : %+v, %v", got, err)
	}
}

func gitAccess(t *testing.T, f Fixture) {
	a, b := f.Code().GitAccess("o/a"), f.Code().GitAccess("o/b")
	if a.URL == "" || a.URL == b.URL {
		t.Fatalf("adresses de clone = %q et %q, attendu une adresse par dépôt", a.URL, b.URL)
	}
	for _, id := range []forge.Identity{a.Identity, a.Agent} {
		if id.Name == "" || !strings.Contains(id.Email, "@") {
			t.Errorf("identité incomplète : %+v", id)
		}
	}
	if a.Identity == a.Agent {
		t.Errorf("night-shift et l'agent signent sous la même identité %+v", a.Identity)
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
