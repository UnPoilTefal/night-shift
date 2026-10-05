package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/github"
)

const token = "ghp_test-secret-token"

func server(t *testing.T, h http.HandlerFunc) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return github.New(srv.URL, token)
}

func TestReadyTicketsSkipsPullRequestsAndReadsBlockersAndComments(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/a/issues":
			if r.URL.Query().Get("labels") != "ready-for-agent" || r.URL.Query().Get("state") != "open" {
				t.Errorf("requête inattendue : %s", r.URL)
			}
			_, _ = io.WriteString(w, `[
				{"number": 1, "title": "t1", "body": "b", "created_at": "2026-10-01T10:00:00Z",
				 "user": {"login": "alice"}, "author_association": "OWNER",
				 "labels": [{"name": "ready-for-agent"}, {"name": "prio:P1"}],
				 "issue_dependencies_summary": {"blocked_by": 2}},
				{"number": 2, "title": "pr", "pull_request": {}, "user": {"login": "bob"}, "labels": []}
			]`)
		case "/repos/o/a/issues/1/comments":
			_, _ = io.WriteString(w, `[
				{"body": "brief", "created_at": "2026-10-02T10:00:00Z", "user": {"login": "bob"}, "author_association": "MEMBER"},
				{"body": "consignes", "created_at": "2026-10-03T10:00:00Z", "updated_at": "2026-10-04T10:00:00Z", "user": {"login": "mallory"}, "author_association": "CONTRIBUTOR"},
				{"body": "c", "created_at": "2026-10-03T11:00:00Z", "user": {"login": "carol"}, "author_association": "COLLABORATOR"}
			]`)
		default:
			t.Errorf("requête inattendue : %s", r.URL)
			http.NotFound(w, r)
		}
	})

	ts, err := c.ReadyTickets(context.Background(), "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("tickets = %+v, attendu un seul (la PR est écartée)", ts)
	}
	got := ts[0]
	if got.Number != 1 || got.Author != "alice" || !got.AuthorAssociated || got.OpenBlockers != 2 || len(got.Labels) != 2 || got.CreatedAt.IsZero() {
		t.Fatalf("ticket mal lu : %+v", got)
	}
	if len(got.Comments) != 3 {
		t.Fatalf("commentaires = %+v, attendu trois", got.Comments)
	}
	if c := got.Comments[0]; c.Author != "bob" || !c.Associated || c.Body != "brief" || c.CreatedAt.IsZero() {
		t.Fatalf("commentaire mal lu : %+v", c)
	}
	if got.Comments[1].Associated || got.Comments[1].UpdatedAt.IsZero() || !got.Comments[2].Associated {
		t.Fatalf("association mal lue : %+v", got.Comments)
	}
}

func TestFileReadsRawContentAndReportsAbsence(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/a/contents/.night-shift/opt-in.yaml":
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Errorf("Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = io.WriteString(w, "version: 1\n")
		default:
			http.NotFound(w, r)
		}
	})

	data, ok, err := c.File(context.Background(), "o/a", ".night-shift/opt-in.yaml")
	if err != nil || !ok || string(data) != "version: 1\n" {
		t.Fatalf("File = %q, %v, %v", data, ok, err)
	}
	if _, ok, err := c.File(context.Background(), "o/b", ".night-shift/opt-in.yaml"); err != nil || ok {
		t.Fatalf("fichier absent : ok = %v, err = %v", ok, err)
	}
}

func TestLabelsAndComments(t *testing.T) {
	var calls []string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.EscapedPath()+" "+strings.TrimSpace(string(body)))
		if r.Method == http.MethodDelete {
			http.NotFound(w, r) // label déjà absent : pas une erreur
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	ctx := context.Background()

	if err := c.AddLabel(ctx, "o/a", 7, "agent-in-progress"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveLabel(ctx, "o/a", 7, "ready for agent"); err != nil {
		t.Fatal(err)
	}
	if err := c.Comment(ctx, "o/a", 7, "réservé"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`POST /repos/o/a/issues/7/labels {"labels":["agent-in-progress"]}`,
		`DELETE /repos/o/a/issues/7/labels/ready%20for%20agent `,
		`POST /repos/o/a/issues/7/comments {"body":"réservé"}`,
	}
	for i := range want {
		if strings.TrimSpace(calls[i]) != strings.TrimSpace(want[i]) {
			t.Fatalf("appel %d = %q, attendu %q", i, calls[i], want[i])
		}
	}
}

func TestErrorsNeverContainTheToken(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message": "Bad credentials: `+token+`"}`)
	})

	_, err := c.ReadyTickets(context.Background(), "o/a")
	if err == nil {
		t.Fatal("erreur attendue")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("le jeton apparaît dans l'erreur : %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("erreur = %v, attendu le statut HTTP", err)
	}
}

func TestOpenDraftPRAlwaysAsksForADraft(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/a/pulls" {
			t.Errorf("requête inattendue : %s %s", r.Method, r.URL)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["draft"] != true || got["head"] != "agent/7-x" || got["base"] != "main" || got["title"] != "T" || got["body"] != "B" {
			t.Errorf("corps = %v", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number": 12, "html_url": "https://github.com/o/a/pull/12"}`)
	})

	n, url, err := c.OpenDraftPR(context.Background(), "o/a", forge.PullRequest{Head: "agent/7-x", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 || url != "https://github.com/o/a/pull/12" {
		t.Fatalf("PR = %d, %q", n, url)
	}
}

func TestCreateIssue(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/a/issues" {
			t.Errorf("requête inattendue : %s %s", r.Method, r.URL)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["title"] != "T" || got["body"] != "B" || len(got) != 2 {
			t.Errorf("corps = %v", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number": 50, "html_url": "https://github.com/o/a/issues/50"}`)
	})

	n, url, err := c.CreateIssue(context.Background(), "o/a", "T", "B")
	if err != nil {
		t.Fatal(err)
	}
	if n != 50 || url != "https://github.com/o/a/issues/50" {
		t.Fatalf("issue = %d, %q", n, url)
	}
}

func TestChecksMergesCheckRunsAndStatusesWithFailureExcerpts(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/a/commits/abc/check-runs":
			_, _ = io.WriteString(w, `{"check_runs": [
				{"id": 1, "name": "test", "status": "completed", "conclusion": "failure", "html_url": "https://ci/1",
				 "output": {"title": "2 tests en échec", "summary": "--- FAIL: TestF", "annotations_count": 1}},
				{"id": 2, "name": "lint", "status": "completed", "conclusion": "success", "output": {"annotations_count": 3}},
				{"id": 3, "name": "e2e", "status": "in_progress", "conclusion": null, "output": {}},
				{"id": 4, "name": "docs", "status": "completed", "conclusion": "skipped", "output": {}},
				{"id": 5, "name": "build", "status": "completed", "conclusion": "timed_out", "output": {"text": "`+strings.Repeat("x", 5000)+`"}}
			]}`)
		case "/repos/o/a/check-runs/1/annotations":
			_, _ = io.WriteString(w, `[{"path": "f_test.go", "start_line": 12, "annotation_level": "failure", "message": "attendu 2, obtenu 3"}]`)
		case "/repos/o/a/commits/abc/status":
			_, _ = io.WriteString(w, `{"statuses": [
				{"context": "zones", "state": "error", "description": "zone interdite touchée", "target_url": "https://ci/z"},
				{"context": "cla", "state": "pending"}
			]}`)
		default:
			t.Errorf("requête inattendue : %s", r.URL)
			http.NotFound(w, r)
		}
	})

	cs, err := c.Checks(context.Background(), "o/a", "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]forge.Check{}
	for _, ch := range cs {
		got[ch.Name] = ch
	}
	want := map[string]forge.CheckState{
		"test": forge.CheckFailed, "lint": forge.CheckPassed, "e2e": forge.CheckPending, "docs": forge.CheckPassed,
		"build": forge.CheckFailed, "zones": forge.CheckFailed, "cla": forge.CheckPending,
	}
	if len(got) != len(want) {
		t.Fatalf("checks = %+v", cs)
	}
	for name, state := range want {
		if got[name].State != state {
			t.Errorf("%s : %s, attendu %s", name, got[name].State, state)
		}
	}
	if e := got["test"].Excerpt; !strings.Contains(e, "2 tests en échec") || !strings.Contains(e, "--- FAIL: TestF") || !strings.Contains(e, "f_test.go:12 : attendu 2, obtenu 3") {
		t.Errorf("extrait de test = %q", e)
	}
	if got["test"].URL != "https://ci/1" || got["zones"].URL != "https://ci/z" || got["zones"].Excerpt != "zone interdite touchée" {
		t.Errorf("checks = %+v", cs)
	}
	if n := len([]rune(got["build"].Excerpt)); n > github.MaxExcerpt+1 {
		t.Errorf("extrait de build : %d caractères, au plus %d", n, github.MaxExcerpt)
	}
}
