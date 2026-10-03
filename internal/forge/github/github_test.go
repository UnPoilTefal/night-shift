package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestReadyTicketsSkipsPullRequestsAndReadsBlockers(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/a/issues" || r.URL.Query().Get("labels") != "ready-for-agent" || r.URL.Query().Get("state") != "open" {
			t.Errorf("requête inattendue : %s", r.URL)
		}
		_, _ = io.WriteString(w, `[
			{"number": 1, "title": "t1", "body": "b", "created_at": "2026-10-01T10:00:00Z",
			 "user": {"login": "alice"}, "labels": [{"name": "ready-for-agent"}, {"name": "prio:P1"}],
			 "issue_dependencies_summary": {"blocked_by": 2}},
			{"number": 2, "title": "pr", "pull_request": {}, "user": {"login": "bob"}, "labels": []}
		]`)
	})

	ts, err := c.ReadyTickets(context.Background(), "o/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("tickets = %+v, attendu un seul (la PR est écartée)", ts)
	}
	got := ts[0]
	if got.Number != 1 || got.Author != "alice" || got.OpenBlockers != 2 || len(got.Labels) != 2 || got.CreatedAt.IsZero() {
		t.Fatalf("ticket mal lu : %+v", got)
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
