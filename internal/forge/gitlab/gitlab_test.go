package gitlab_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab/gitlabtest"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
)

var day = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ready(iid int, author string) gitlabtest.Issue {
	return gitlabtest.Issue{IID: iid, Title: "t", Author: author, Labels: []string{forge.LabelReady}, CreatedAt: day}
}

func readyTickets(t *testing.T, f *fixture, repo string) []forge.Ticket {
	t.Helper()
	ts, err := f.client.ReadyTickets(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestAProjectInSubgroupsIsAddressedByItsEncodedPath(t *testing.T) {
	f := newFixture(t)
	f.srv.AddIssue("g/sub/p", ready(1, "alice"))
	f.srv.SetFile("g/sub/p", ".night-shift/opt-in.yaml", "version: 1\n")

	if ts := readyTickets(t, f, "g/sub/p"); len(ts) != 1 || ts[0].Repo != "g/sub/p" || ts[0].Number != 1 {
		t.Fatalf("tickets = %+v", ts)
	}
	data, ok, err := f.client.File(context.Background(), "g/sub/p", ".night-shift/opt-in.yaml")
	if err != nil || !ok || string(data) != "version: 1\n" {
		t.Fatalf("File = %q, %v, %v", data, ok, err)
	}
	calls := f.srv.Calls()
	for _, want := range []string{
		"GET /projects/g%2Fsub%2Fp/issues",
		"GET /projects/g%2Fsub%2Fp/repository/files/.night-shift%2Fopt-in.yaml/raw",
	} {
		if !slices.Contains(calls, want) {
			t.Errorf("appel %q absent de %q", want, calls)
		}
	}
	if got := f.client.GitAccess("g/sub/p").URL; got != "https://gitlab.example/g/sub/p.git" {
		t.Errorf("adresse de clone = %q", got)
	}
}

func TestBlockedByLinesAndNativeLinksCountOpenBlockersOnce(t *testing.T) {
	f := newFixture(t)
	for _, i := range []gitlabtest.Issue{
		{IID: 2, Title: "ouverte, liée et nommée", Author: "alice"},
		{IID: 3, Title: "fermée", Author: "alice", Closed: true},
		{IID: 4, Title: "ouverte, nommée seulement", Author: "alice"},
		{IID: 6, Title: "seulement en relation", Author: "alice"},
	} {
		f.srv.AddIssue("o/a", i)
	}
	f.srv.AddIssue("o/autre", gitlabtest.Issue{IID: 2, Title: "ouverte, d'un autre projet", Author: "alice"})
	t1 := ready(1, "alice")
	t1.Description = "Le brief.\n\nBlocked by: #2, #3\nBlocked by: #4, #5"
	t1.Links = []gitlabtest.Link{
		{Type: "is_blocked_by", Repo: "o/a", IID: 2},
		{Type: "is_blocked_by", Repo: "o/a", IID: 3},
		{Type: "is_blocked_by", Repo: "o/autre", IID: 2},
		{Type: "relates_to", Repo: "o/a", IID: 6},
		{Type: "blocks", Repo: "o/a", IID: 6},
	}
	f.srv.AddIssue("o/a", t1)
	t7 := ready(7, "alice")
	t7.Description = "Blocked by: #3, #7"
	f.srv.AddIssue("o/a", t7)

	got := map[int]int{}
	for _, tk := range readyTickets(t, f, "o/a") {
		got[tk.Number] = tk.OpenBlockers
	}
	// #1 : #2 (lien et ligne), #4 (ligne), o/autre#2 (lien) ; #3 est fermée,
	// #5 n'existe pas, #6 n'est pas un bloqueur. #7 ne se bloque pas
	// lui-même.
	if got[1] != 3 || got[7] != 0 {
		t.Fatalf("bloqueurs ouverts = %v, attendu #1 : 3, #7 : 0", got)
	}
}

func TestBlockedByLineCountsWithoutNativeLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.EscapedPath(); {
		case p == "/projects/o%2Fa/issues" && r.URL.Query().Get("labels") == forge.LabelReady:
			_, _ = io.WriteString(w, `[{"iid": 1, "description": "Blocked by: #2", "state": "opened", "author": {"id": 1}}]`)
		case p == "/projects/o%2Fa/issues/1/links":
			// Une édition sans liens de blocage ne sert pas la route.
			http.NotFound(w, r)
		case p == "/projects/o%2Fa/issues/2":
			_, _ = io.WriteString(w, `{"iid": 2, "state": "opened", "web_url": "https://g/o/a/-/issues/2"}`)
		case p == "/projects/o%2Fa/members/all/1":
			_, _ = io.WriteString(w, `{"access_level": 30}`)
		case p == "/projects/o%2Fa/issues/1/notes":
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Errorf("requête inattendue : %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ts, err := gitlab.New(srv.URL, "https://gitlab.example", token).ReadyTickets(context.Background(), "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].OpenBlockers != 1 {
		t.Fatalf("tickets = %+v, attendu un bloqueur", ts)
	}
}

func TestOpenChangeRequestIsADraftMergeRequestThatKeepsItsBranch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	n, url, err := f.client.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{
		Head: "agent/7-x", Base: "main", Title: "Ajouter f", Body: "Closes #7\n\nPR ouverte en brouillon.", Labels: []string{forge.LabelSensitiveZone},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Draft: déjà", "[draft] déjà", "(Draft) déjà", "draft: déjà", strings.Repeat("x", 300)} {
		if _, _, err := f.client.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{Head: "agent/8-y", Base: "main", Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	mrs := f.srv.MergeRequests("o/a")
	m := mrs[0]
	if n != 1 || url != "https://gitlab.example/o/a/-/merge_requests/1" {
		t.Fatalf("merge request = %d, %q", n, url)
	}
	if m.Title != "Draft: Ajouter f" || !strings.HasPrefix(m.Description, "Closes #7\n") || m.SourceBranch != "agent/7-x" || m.TargetBranch != "main" {
		t.Fatalf("merge request = %+v", m)
	}
	if m.RemoveSourceBranch || !slices.Equal(m.Labels, []string{forge.LabelSensitiveZone}) {
		t.Fatalf("merge request = %+v, attendu sa branche gardée et son label", m)
	}
	for _, m := range mrs[1:5] {
		if !m.Draft() || strings.Count(strings.ToLower(m.Title), "draft") != 1 {
			t.Errorf("titre = %q, attendu une seule marque de brouillon", m.Title)
		}
	}
	if long := []rune(mrs[5].Title); len(long) != 255 || !mrs[5].Draft() || long[254] != '…' {
		t.Errorf("titre long = %d caractères, attendu 255, marqué brouillon et tronqué", len(long))
	}
}

func TestMissingLabelsAreCreatedOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.srv.AddIssue("o/a", ready(1, "alice"))
	f.srv.DefineLabel("o/a", forge.LabelHuman)
	n, _, err := f.client.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{Head: "agent/1-x", Base: "main", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		for _, l := range []string{forge.LabelInProgress, forge.LabelHuman} {
			if err := f.client.AddLabel(ctx, "o/a", 1, l); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.client.AddChangeRequestLabel(ctx, "o/a", n, forge.LabelInProgress); err != nil {
			t.Fatal(err)
		}
	}

	var created int
	for _, c := range f.srv.Calls() {
		if c == "POST /projects/o%2Fa/labels" {
			created++
		}
	}
	// Un appel pour créer agent-in-progress, un pour constater que
	// ready-for-human existe déjà.
	if created != 2 {
		t.Fatalf("créations de label demandées = %d, attendu 2\n%q", created, f.srv.Calls())
	}
	if got := f.srv.Issue("o/a", 1).Labels; !slices.Equal(got, []string{forge.LabelReady, forge.LabelInProgress, forge.LabelHuman}) {
		t.Fatalf("labels du ticket = %q", got)
	}
}

func TestOnlyDevelopersDirectOrInheritedAreAssociated(t *testing.T) {
	f := newFixture(t)
	f.srv.AddMember("o/a", "reporter", 20, false)
	f.srv.AddMember("o/a", "heritee", gitlab.Developer, true)
	f.srv.AddMember("o/a", "mainteneuse", 40, false)
	f.srv.AddMember("o/a", "mixte", 20, false)
	f.srv.AddMember("o/a", "mixte", 40, true)
	i := ready(1, "reporter")
	for _, a := range []string{"heritee", "mainteneuse", "mixte", "inconnu", "reporter"} {
		i.Notes = append(i.Notes, gitlabtest.Note{Author: a, Body: a, CreatedAt: day})
	}
	f.srv.AddIssue("o/a", i)
	f.srv.AddIssue("o/a", ready(2, "heritee"))

	got := map[string]bool{}
	for _, tk := range readyTickets(t, f, "o/a") {
		got["#"+tk.Title+tk.Author] = tk.AuthorAssociated
		for _, c := range tk.Comments {
			got[c.Author] = c.Associated
		}
	}
	want := map[string]bool{
		"#treporter": false, "#theritee": true,
		"heritee": true, "mainteneuse": true, "mixte": true, "inconnu": false, "reporter": false,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s associé = %v, attendu %v", k, got[k], v)
		}
	}
}

func TestSystemNotesAreNotComments(t *testing.T) {
	f := newFixture(t)
	f.srv.AddMember("o/a", "alice", gitlab.Developer, false)
	i := ready(1, "alice")
	i.Notes = []gitlabtest.Note{
		{Author: "alice", Body: "précision", CreatedAt: day},
		{Author: "mallory", Body: "added ~ready-for-agent label", System: true, CreatedAt: day.Add(time.Hour)},
		{Author: "alice", Body: "corrigé", CreatedAt: day.Add(2 * time.Hour), UpdatedAt: day.Add(3 * time.Hour)},
	}
	f.srv.AddIssue("o/a", i)

	ts := readyTickets(t, f, "o/a")
	if len(ts) != 1 || len(ts[0].Comments) != 2 {
		t.Fatalf("tickets = %+v, attendu deux commentaires", ts)
	}
	cs := ts[0].Comments
	if cs[0].Body != "précision" || !cs[0].UpdatedAt.IsZero() || cs[1].Body != "corrigé" || !cs[1].UpdatedAt.Equal(day.Add(3*time.Hour)) {
		t.Fatalf("commentaires = %+v ; attendu une date de modification pour la seule note modifiée", cs)
	}
}

func TestChecksReadTheLatestPipelineAndTheEndOfFailedJobLogs(t *testing.T) {
	f := newFixture(t)
	long := "début\n" + strings.Repeat("ligne de log\n", 400) + "\x1b[31;1m--- FAIL: TestF\x1b[0;m\n" +
		"section_end:1700000000:step_script\r\x1b[0K\x1b[31;1mERROR: Job failed: exit code 1\x1b[0;m\n"
	f.srv.SetPipeline("o/a", "abc", "failed",
		gitlabtest.Job{Name: "test", Status: "failed", WebURL: "https://ci/1", Trace: long},
		gitlabtest.Job{Name: "lint", Status: "failed", AllowFailure: true, Trace: "ignoré"},
		gitlabtest.Job{Name: "deploy", Status: "manual", AllowFailure: true},
		gitlabtest.Job{Name: "release", Status: "manual"},
		gitlabtest.Job{Name: "e2e", Status: "running"},
		gitlabtest.Job{Name: "docs", Status: "skipped"},
		gitlabtest.Job{Name: "build", Status: "canceled", Trace: "court"},
		gitlabtest.Job{Name: "minified", Status: "failed", Trace: strings.Repeat("y", 5000)},
		gitlabtest.Job{Name: "child", Status: "failed", WebURL: "https://ci/child", Downstream: "failed"},
		gitlabtest.Job{Name: "aval", Status: "success", Downstream: "success"},
	)
	f.srv.SetPipeline("o/a", "yaml", "failed")

	cs, err := f.client.Checks(context.Background(), "o/a", "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]forge.Check{}
	for _, c := range cs {
		got[c.Name] = c
	}
	want := map[string]forge.CheckState{
		"test": forge.CheckFailed, "lint": forge.CheckPassed, "deploy": forge.CheckPassed, "release": forge.CheckPending,
		"e2e": forge.CheckPending, "docs": forge.CheckPassed, "build": forge.CheckFailed,
		"minified": forge.CheckFailed, "child": forge.CheckFailed, "aval": forge.CheckPassed,
	}
	if len(got) != len(want) {
		t.Fatalf("checks = %+v", cs)
	}
	for name, state := range want {
		if got[name].State != state {
			t.Errorf("%s : %s, attendu %s", name, got[name].State, state)
		}
	}
	e := got["test"].Excerpt
	if !strings.HasPrefix(e, "…\nligne de log\n") || !strings.HasSuffix(e, "--- FAIL: TestF\nERROR: Job failed: exit code 1") {
		t.Errorf("extrait de test = %q", e)
	}
	if strings.Contains(e, "\x1b") || strings.Contains(e, "section_end") || strings.Contains(e, "début") || len([]rune(e)) > gitlab.MaxExcerpt {
		t.Errorf("extrait de test mal nettoyé ou trop long (%d caractères)", len([]rune(e)))
	}
	if got["test"].URL != "https://ci/1" || got["build"].Excerpt != "court" || got["lint"].Excerpt != "" {
		t.Errorf("checks = %+v", cs)
	}
	if n := len([]rune(got["minified"].Excerpt)); n > gitlab.MaxExcerpt {
		t.Errorf("extrait d'un log sans saut de ligne : %d caractères, au plus %d", n, gitlab.MaxExcerpt)
	}
	if e := got["child"].Excerpt; got["child"].URL != "https://ci/child" || !strings.Contains(e, "failed") || !strings.Contains(e, "/-/pipelines/") {
		t.Errorf("job de déclenchement = %+v, attendu l'adresse de la pipeline aval", got["child"])
	}

	cs, err = f.client.Checks(context.Background(), "o/a", "yaml")
	if err != nil || len(cs) != 1 || cs[0].Name != "pipeline" || cs[0].State != forge.CheckFailed || cs[0].Excerpt == "" {
		t.Fatalf("pipeline sans job : %+v, %v", cs, err)
	}
}

func TestGitAccessPresentsTheTokenAsOauth2WithNoreplyIdentitiesOfTheInstance(t *testing.T) {
	got := gitlab.New(gitlab.DefaultBaseURL, "https://gitlab.interne.example/", token).GitAccess("g/sub/p")
	want := forge.GitAccess{
		URL:      "https://gitlab.interne.example/g/sub/p.git",
		User:     "oauth2",
		Identity: forge.Identity{Name: "night-shift", Email: "night-shift@users.noreply.gitlab.interne.example"},
		Agent:    forge.Identity{Name: "night-shift agent", Email: "night-shift-agent@users.noreply.gitlab.interne.example"},
	}
	if got != want {
		t.Fatalf("GitAccess = %+v\nattendu %+v", got, want)
	}
}

func TestTheTokenNeverAppearsInAnErrorFromTheAPIOrFromGit(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("PRIVATE-TOKEN")+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message": "401 Unauthorized: `+token+`"}`)
	}))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, srv.URL, token)

	_, apiErr := c.ReadyTickets(context.Background(), "o/a")
	_, _, gitErr := gitrepo.Remote{GitAccess: c.GitAccess("o/a"), Token: token}.Clone(context.Background(), t.TempDir()+"/clone")

	if apiErr == nil || gitErr == nil {
		t.Fatalf("erreurs attendues : API %v, git %v", apiErr, gitErr)
	}
	if !strings.Contains(apiErr.Error(), "401") {
		t.Errorf("erreur de l'API = %v, attendu le statut HTTP", apiErr)
	}
	for _, err := range []error{apiErr, gitErr} {
		if strings.Contains(err.Error(), token) {
			t.Errorf("le jeton apparaît dans l'erreur : %v", err)
		}
	}
	// git a bien présenté le jeton, en Basic avec l'utilisateur oauth2.
	if !slices.ContainsFunc(auth, func(a string) bool { return strings.HasPrefix(a, "Basic ") }) {
		t.Errorf("en-têtes reçus = %q, attendu une authentification de git", auth)
	}
}

func TestChecksFallBackToTheHeadPipelineOfTheMergeRequest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	n, _, err := f.client.OpenChangeRequest(ctx, "o/a", forge.ChangeRequest{Head: "agent/7-x", Base: "main", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	// Une pipeline de résultats fusionnés porte le commit de fusion, pas la
	// tête de la branche.
	f.srv.SetMergeRequestPipeline("o/a", n, "abc", "failed", gitlabtest.Job{Name: "test", Status: "failed", Trace: "--- FAIL: TestF"})

	cs, err := f.client.Checks(ctx, "o/a", "abc")
	if err != nil || len(cs) != 1 || cs[0].State != forge.CheckFailed || cs[0].Excerpt != "--- FAIL: TestF" {
		t.Fatalf("checks = %+v, %v ; attendu la pipeline de tête de la merge request", cs, err)
	}
	if cs, err := f.client.Checks(ctx, "o/a", "autre"); err != nil || len(cs) != 0 {
		t.Fatalf("commit étranger à la merge request : %+v, %v", cs, err)
	}
}

func TestAnUnreadableLogKeepsTheFailureAndAReadLogIsNotReadAgain(t *testing.T) {
	var traces int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.EscapedPath(); p {
		case "/projects/o%2Fa/pipelines":
			_, _ = io.WriteString(w, `[{"id": 1, "status": "failed"}]`)
		case "/projects/o%2Fa/pipelines/1/jobs":
			_, _ = io.WriteString(w, `[{"id": 10, "name": "test", "status": "failed"}, {"id": 11, "name": "lint", "status": "failed"}]`)
		case "/projects/o%2Fa/pipelines/1/bridges":
			_, _ = io.WriteString(w, `[]`)
		case "/projects/o%2Fa/jobs/10/trace":
			traces++
			_, _ = io.WriteString(w, "--- FAIL: TestF")
		case "/projects/o%2Fa/jobs/11/trace":
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Errorf("requête inattendue : %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, "https://gitlab.example", token)

	for range 2 {
		cs, err := c.Checks(context.Background(), "o/a", "abc")
		if err != nil || len(cs) != 2 {
			t.Fatalf("checks = %+v, %v", cs, err)
		}
		if cs[0].Excerpt != "--- FAIL: TestF" || cs[1].State != forge.CheckFailed || !strings.Contains(cs[1].Excerpt, "403") {
			t.Fatalf("checks = %+v ; attendu l'échec gardé et le log illisible signalé", cs)
		}
	}
	if traces != 1 {
		t.Fatalf("log lu %d fois, attendu une seule", traces)
	}
}
