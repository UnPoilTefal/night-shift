package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/forgetest"
	"github.com/UnPoilTefal/night-shift/internal/forge/github"
)

func TestContract(t *testing.T) {
	forgetest.Run(t, func(t *testing.T) forgetest.Fixture { return newAPI(t) })
}

// api est une API REST GitHub simulée, à état, qui sert les routes dont
// l'adaptateur se sert : issues et PR partagent la numérotation d'un dépôt,
// et poser un label inconnu le crée sur le dépôt, comme sur github.com.
type api struct {
	client *github.Client

	mu     sync.Mutex
	issues map[string][]*ghIssue
	files  map[string]string
	checks map[string][]forge.Check
	labels map[string][]string
}

type ghIssue struct {
	forge.Ticket
	closed   bool
	pull     *forge.ChangeRequest
	draft    bool
	comments []forge.Comment
	// updated est la dernière mise à jour ; state, closedAt et commits
	// décrivent une PR.
	updated  time.Time
	state    forge.ChangeRequestState
	closedAt time.Time
	commits  []string
}

func newAPI(t *testing.T) *api {
	a := &api{issues: map[string][]*ghIssue{}, files: map[string]string{}, checks: map[string][]forge.Check{}, labels: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.serve(t, w, r)
	}))
	t.Cleanup(srv.Close)
	a.client = github.New(srv.URL, "https://git.example", token)
	return a
}

func (a *api) Tickets() forge.TicketSource { return a.client }
func (a *api) Code() forge.CodeForge       { return a.client }
func (a *api) Sandbox() forge.Sandbox      { return a.client }

func (a *api) AddTicket(t forge.Ticket, closed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	updated := t.CreatedAt
	for _, c := range t.Comments {
		if c.CreatedAt.After(updated) {
			updated = c.CreatedAt
		}
		if c.UpdatedAt.After(updated) {
			updated = c.UpdatedAt
		}
	}
	a.issues[t.Repo] = append(a.issues[t.Repo], &ghIssue{Ticket: t, closed: closed, comments: slices.Clone(t.Comments), updated: updated})
	for _, l := range t.Labels {
		a.define(t.Repo, l)
	}
}

func (a *api) SetChange(repo string, number int, state forge.ChangeRequestState, at time.Time, commits ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.find(repo, number)
	i.state, i.closedAt, i.commits = state, at, commits
	i.closed = state != forge.ChangeOpen
}

func (a *api) SetFile(repo, path, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.files[repo+":"+path] = content
}

func (a *api) SetChecks(repo, sha string, checks ...forge.Check) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[repo+"@"+sha] = checks
}

func (a *api) Ticket(repo string, number int) forgetest.Ticket {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.find(repo, number)
	if i == nil || i.pull != nil {
		panic(fmt.Sprintf("issue %s#%d inconnue", repo, number))
	}
	return forgetest.Ticket{Title: i.Title, Body: i.Body, Labels: slices.Clone(i.Labels), Comments: bodies(i.comments), Closed: i.closed}
}

func (a *api) ChangeRequest(repo string, number int) forgetest.ChangeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.find(repo, number)
	if i == nil || i.pull == nil {
		panic(fmt.Sprintf("PR %s#%d inconnue", repo, number))
	}
	cr := *i.pull
	cr.Labels = slices.Clone(i.Labels)
	return forgetest.ChangeRequest{ChangeRequest: cr, Draft: i.draft, Comments: bodies(i.comments), Closed: i.state == forge.ChangeClosed}
}

func (a *api) RepoLabels(repo string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.labels[repo])
}

func (a *api) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/repos/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) < 3 {
		t.Errorf("requête inattendue : %s %s", r.Method, r.URL)
		http.NotFound(w, r)
		return
	}
	repo, route := parts[0]+"/"+parts[1], parts[2:]
	switch {
	case r.Method == http.MethodGet && route[0] == "contents":
		path, _ := url.PathUnescape(strings.Join(route[1:], "/"))
		content, ok := a.files[repo+":"+path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	case r.Method == http.MethodGet && len(route) == 1 && route[0] == "issues":
		a.listIssues(w, r, repo)
	case r.Method == http.MethodPost && len(route) == 1 && route[0] == "issues":
		var body struct {
			Title, Body string
			Labels      []string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		i := &ghIssue{Ticket: forge.Ticket{Repo: repo, Number: a.next(repo), Title: body.Title, Body: body.Body, Labels: body.Labels}, updated: time.Now()}
		for _, l := range body.Labels {
			a.define(repo, l)
		}
		a.issues[repo] = append(a.issues[repo], i)
		created(w, i.Number, "https://github.example/"+repo+"/issues/"+strconv.Itoa(i.Number))
	case r.Method == http.MethodPatch && len(route) == 2 && route[0] == "issues":
		n, _ := strconv.Atoi(route[1])
		i := a.find(repo, n)
		if i == nil {
			http.NotFound(w, r)
			return
		}
		var body struct{ Body, State *string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Body != nil {
			i.Body = *body.Body
		}
		if body.State != nil {
			i.closed = *body.State == "closed"
		}
		i.updated = time.Now()
		_ = json.NewEncoder(w).Encode(map[string]any{"number": i.Number, "html_url": "https://github.example/" + repo + "/issues/" + strconv.Itoa(i.Number)})
	case r.Method == http.MethodGet && len(route) == 2 && route[0] == "issues":
		n, _ := strconv.Atoi(route[1])
		i := a.find(repo, n)
		if i == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(issueJSON(repo, i))
	case r.Method == http.MethodPatch && len(route) == 2 && route[0] == "pulls":
		n, _ := strconv.Atoi(route[1])
		i := a.find(repo, n)
		if i == nil || i.pull == nil {
			http.NotFound(w, r)
			return
		}
		var body struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.State == "closed" && i.state != forge.ChangeMerged {
			i.closed, i.state, i.closedAt, i.updated = true, forge.ChangeClosed, time.Now(), time.Now()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": i.Number, "state": "closed"})
	case r.Method == http.MethodGet && len(route) == 1 && route[0] == "pulls":
		a.listPulls(w, r, repo)
	case r.Method == http.MethodGet && len(route) == 3 && route[0] == "pulls" && route[2] == "commits":
		n, _ := strconv.Atoi(route[1])
		i := a.find(repo, n)
		if i == nil || i.pull == nil {
			http.NotFound(w, r)
			return
		}
		out := []map[string]any{}
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page <= 1 {
			for _, m := range i.commits {
				out = append(out, map[string]any{"commit": map[string]string{"message": m}})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && len(route) == 1 && route[0] == "pulls":
		var body struct {
			Head, Base, Title, Body string
			Draft                   bool
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		i := &ghIssue{
			Ticket:  forge.Ticket{Repo: repo, Number: a.next(repo), Title: body.Title, Body: body.Body},
			pull:    &forge.ChangeRequest{Head: body.Head, Base: body.Base, Title: body.Title, Body: body.Body},
			draft:   body.Draft,
			updated: time.Now(),
			state:   forge.ChangeOpen,
		}
		i.CreatedAt = time.Now()
		a.issues[repo] = append(a.issues[repo], i)
		created(w, i.Number, "https://github.example/"+repo+"/pull/"+strconv.Itoa(i.Number))
	case route[0] == "issues" && len(route) >= 3:
		a.issueRoute(w, r, repo, route[1:])
	case r.Method == http.MethodGet && route[0] == "commits" && len(route) == 3:
		a.commitRoute(w, repo, route[1], route[2])
	default:
		t.Errorf("requête inattendue : %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (a *api) listIssues(w http.ResponseWriter, r *http.Request, repo string) {
	q := r.URL.Query()
	out := []map[string]any{}
	since, _ := time.Parse(time.RFC3339, q.Get("since"))
	if page, _ := strconv.Atoi(q.Get("page")); page <= 1 {
		for _, i := range a.issues[repo] {
			if (q.Get("state") == "open" && i.closed) || (q.Get("labels") != "" && !slices.Contains(i.Labels, q.Get("labels"))) || i.updated.Before(since) {
				continue
			}
			out = append(out, issueJSON(repo, i))
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// issueJSON rend une issue ou une PR telle que l'API des issues la sert.
func issueJSON(repo string, i *ghIssue) map[string]any {
	type label struct {
		Name string `json:"name"`
	}
	var labels []label
	for _, l := range i.Labels {
		labels = append(labels, label{l})
	}
	association := "NONE"
	if i.AuthorAssociated {
		association = "OWNER"
	}
	e := map[string]any{
		"number": i.Number, "html_url": "https://github.example/" + repo + "/issues/" + strconv.Itoa(i.Number),
		"title": i.Title, "body": i.Body, "created_at": i.CreatedAt,
		"user": map[string]string{"login": i.Author}, "author_association": association, "labels": labels,
		"issue_dependencies_summary": map[string]int{"blocked_by": i.OpenBlockers},
	}
	if i.pull != nil {
		e["pull_request"] = map[string]any{}
	}
	return e
}

// listPulls sert les PR d'un dépôt, les plus récentes d'abord.
func (a *api) listPulls(w http.ResponseWriter, r *http.Request, repo string) {
	out := []map[string]any{}
	if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page <= 1 {
		for _, i := range slices.Backward(a.issues[repo]) {
			if i.pull == nil {
				continue
			}
			e := map[string]any{
				"number": i.Number, "html_url": "https://github.example/" + repo + "/pull/" + strconv.Itoa(i.Number),
				"state": "open", "created_at": i.CreatedAt, "closed_at": nil, "merged_at": nil,
				"head": map[string]string{"ref": i.pull.Head},
			}
			if i.state != forge.ChangeOpen {
				e["state"], e["closed_at"] = "closed", i.closedAt
			}
			if i.state == forge.ChangeMerged {
				e["merged_at"] = i.closedAt
			}
			out = append(out, e)
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *api) issueRoute(w http.ResponseWriter, r *http.Request, repo string, route []string) {
	n, _ := strconv.Atoi(route[0])
	i := a.find(repo, n)
	if i == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && route[1] == "comments":
		out := []map[string]any{}
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page <= 1 {
			for _, c := range i.comments {
				association := "CONTRIBUTOR"
				if c.Associated {
					association = "MEMBER"
				}
				e := map[string]any{"body": c.Body, "created_at": c.CreatedAt, "user": map[string]string{"login": c.Author}, "author_association": association}
				if !c.UpdatedAt.IsZero() {
					e["updated_at"] = c.UpdatedAt
				}
				out = append(out, e)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && route[1] == "comments":
		var body struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		i.comments = append(i.comments, forge.Comment{Author: "night-shift", Body: body.Body, CreatedAt: time.Now()})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case r.Method == http.MethodPost && route[1] == "labels" && len(route) == 2:
		var body struct{ Labels []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, l := range body.Labels {
			a.define(repo, l)
			if !slices.Contains(i.Labels, l) {
				i.Labels = append(i.Labels, l)
			}
		}
		_ = json.NewEncoder(w).Encode(i.Labels)
	case r.Method == http.MethodDelete && route[1] == "labels" && len(route) == 3:
		l, _ := url.PathUnescape(route[2])
		if !slices.Contains(i.Labels, l) {
			http.NotFound(w, r)
			return
		}
		i.Labels = slices.DeleteFunc(i.Labels, func(s string) bool { return s == l })
		_ = json.NewEncoder(w).Encode(i.Labels)
	default:
		http.NotFound(w, r)
	}
}

// commitRoute sert les checks d'un commit en check runs ; les statuts de
// commit restent vides.
func (a *api) commitRoute(w http.ResponseWriter, repo, sha, kind string) {
	switch kind {
	case "check-runs":
		runs := []map[string]any{}
		for n, c := range a.checks[repo+"@"+sha] {
			run := map[string]any{"id": n + 1, "name": c.Name, "html_url": c.URL, "status": "completed", "output": map[string]any{"summary": c.Excerpt}}
			switch c.State {
			case forge.CheckPending:
				run["status"], run["conclusion"] = "in_progress", nil
			case forge.CheckPassed:
				run["conclusion"] = "success"
			case forge.CheckFailed:
				run["conclusion"] = "failure"
			}
			runs = append(runs, run)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": runs})
	case "status":
		_, _ = w.Write([]byte(`{"statuses": []}`))
	}
}

func (a *api) find(repo string, number int) *ghIssue {
	for _, i := range a.issues[repo] {
		if i.Number == number {
			return i
		}
	}
	return nil
}

func (a *api) next(repo string) int {
	n := 1
	for _, i := range a.issues[repo] {
		n = max(n, i.Number+1)
	}
	return n
}

func (a *api) define(repo, label string) {
	if !slices.Contains(a.labels[repo], label) {
		a.labels[repo] = append(a.labels[repo], label)
	}
}

func created(w http.ResponseWriter, number int, url string) {
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"number": number, "html_url": url})
}

func bodies(cs []forge.Comment) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Body)
	}
	return out
}
