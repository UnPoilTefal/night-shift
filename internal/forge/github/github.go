// Package github est l'adaptateur GitHub de la source de tickets et de la
// forge de code, par l'API REST, git en HTTPS et un jeton fourni à
// l'exécution.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// DefaultBaseURL est l'URL de l'API REST de github.com.
const DefaultBaseURL = "https://api.github.com"

// DefaultGitURL est la base des adresses de clone de github.com.
const DefaultGitURL = "https://github.com"

// Instance rend les adresses de l'API et des clones d'une instance GitHub
// Enterprise Server servie à base.
func Instance(base string) (apiURL, gitURL string) {
	base = strings.TrimSuffix(base, "/")
	return base + "/api/v3", base
}

// ToolTokenEnv sont les variables où les outils GitHub (gh, actions)
// cherchent un jeton.
var ToolTokenEnv = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}

// TokenPrefixes sont les préfixes des jetons GitHub.
var TokenPrefixes = []string{"ghp_", "github_pat_", "gho_", "ghs_", "ghu_"}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepo vérifie qu'un dépôt est désigné comme GitHub l'attend :
// propriétaire/nom.
func ValidRepo(repo string) error {
	if !repoName.MatchString(repo) || strings.Contains(repo, "..") {
		return fmt.Errorf("dépôt %q invalide, attendu propriétaire/nom", repo)
	}
	return nil
}

// Client implémente forge.TicketSource et forge.CodeForge pour GitHub.
type Client struct {
	baseURL string
	gitURL  string
	token   string
	http    *http.Client
}

// New crée un client sur l'API baseURL ; gitURL est la base des adresses de
// clone (DefaultGitURL, ou un dossier de dépôts nus pour les tests). Le
// jeton n'apparaît jamais dans une erreur.
func New(baseURL, gitURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		gitURL:  strings.TrimSuffix(gitURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// GitAccess implémente forge.CodeForge. GitHub attend le jeton avec
// l'utilisateur x-access-token, et les adresses noreply signent les commits
// sans désigner de compte.
func (c *Client) GitAccess(repo string) forge.GitAccess {
	return forge.GitAccess{
		URL:      c.gitURL + "/" + repo + ".git",
		User:     "x-access-token",
		Identity: forge.Identity{Name: "night-shift", Email: "night-shift@users.noreply.github.com"},
		Agent:    forge.Identity{Name: "night-shift agent", Email: "night-shift-agent@users.noreply.github.com"},
	}
}

type issue struct {
	Number      int       `json:"number"`
	HTMLURL     string    `json:"html_url"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	PullRequest *struct{} `json:"pull_request"`
	User        user      `json:"user"`
	Association string    `json:"author_association"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Dependencies *struct {
		BlockedBy int `json:"blocked_by"`
	} `json:"issue_dependencies_summary"`
}

type user struct {
	Login string `json:"login"`
}

type comment struct {
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	User        user      `json:"user"`
	Association string    `json:"author_association"`
}

// associated dit si une association GitHub vaut accès au dépôt : les
// contributeurs passés et les inconnus n'en ont pas.
func associated(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// ReadyTickets implémente forge.TicketSource. Les PR, que l'API des issues renvoie
// aussi, sont écartées.
func (c *Client) ReadyTickets(ctx context.Context, repo string) ([]forge.Ticket, error) {
	var ts []forge.Ticket
	q := url.Values{"state": {"open"}, "labels": {forge.LabelReady}, "per_page": {"100"}}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var batch []issue
		if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, i := range batch {
			if i.PullRequest != nil {
				continue
			}
			t, err := c.ticket(ctx, repo, i)
			if err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		if len(batch) < 100 {
			return ts, nil
		}
	}
}

// ticket traduit une issue en ticket, avec ses commentaires.
func (c *Client) ticket(ctx context.Context, repo string, i issue) (forge.Ticket, error) {
	t := forge.Ticket{
		Repo: repo, Number: i.Number, Title: i.Title, Body: i.Body,
		Author: i.User.Login, AuthorAssociated: associated(i.Association), CreatedAt: i.CreatedAt,
	}
	for _, l := range i.Labels {
		t.Labels = append(t.Labels, l.Name)
	}
	if i.Dependencies != nil {
		t.OpenBlockers = i.Dependencies.BlockedBy
	}
	comments, err := c.comments(ctx, repo, i.Number)
	if err != nil {
		return forge.Ticket{}, err
	}
	t.Comments = comments
	return t, nil
}

// UpdatedTickets implémente forge.TicketSource par le filtre since de l'API
// des issues, qui porte sur leur date de mise à jour ; les PR sont écartées.
func (c *Client) UpdatedTickets(ctx context.Context, repo string, since time.Time) ([]forge.Ticket, error) {
	var ts []forge.Ticket
	q := url.Values{"state": {"all"}, "since": {since.UTC().Format(time.RFC3339)}, "per_page": {"100"}}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var batch []issue
		if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, i := range batch {
			if i.PullRequest != nil {
				continue
			}
			t, err := c.ticket(ctx, repo, i)
			if err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		if len(batch) < 100 {
			return ts, nil
		}
	}
}

// Ticket implémente forge.Sandbox.
func (c *Client) Ticket(ctx context.Context, repo string, number int) (forge.Ticket, error) {
	var i issue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repo, number), nil, &i); err != nil {
		return forge.Ticket{}, err
	}
	if i.PullRequest != nil {
		return forge.Ticket{}, fmt.Errorf("%s#%d est une pull request, pas un ticket", repo, number)
	}
	return c.ticket(ctx, repo, i)
}

// CloseTicket implémente forge.Sandbox.
func (c *Client) CloseTicket(ctx context.Context, repo string, number int) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, number), map[string]string{"state": "closed"}, nil)
}

// CloseChangeRequest implémente forge.Sandbox.
func (c *Client) CloseChangeRequest(ctx context.Context, repo string, number int) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), map[string]string{"state": "closed"}, nil)
}

// PutIssue implémente forge.TicketSource : l'issue ouverte de même label et
// de même titre voit son corps remplacé ; sinon une issue est ouverte avec
// le label, que GitHub crée s'il n'existe pas.
func (c *Client) PutIssue(ctx context.Context, repo, label, title, body string) (int, string, error) {
	q := url.Values{"state": {"open"}, "labels": {label}, "per_page": {"100"}}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var batch []issue
		if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return 0, "", err
		}
		for _, i := range batch {
			if i.PullRequest == nil && i.Title == title {
				var out issue
				if err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, i.Number), map[string]string{"body": body}, &out); err != nil {
					return 0, "", err
				}
				return out.Number, out.HTMLURL, nil
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	var out issue
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/issues", map[string]any{"title": title, "body": body, "labels": []string{label}}, &out); err != nil {
		return 0, "", err
	}
	return out.Number, out.HTMLURL, nil
}

type pull struct {
	Number    int        `json:"number"`
	HTMLURL   string     `json:"html_url"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	MergedAt  *time.Time `json:"merged_at"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// AgentChangeRequests implémente forge.CodeForge par les PR du dépôt, les
// plus récentes d'abord ; une PR fermée avec une date de merge est mergée.
func (c *Client) AgentChangeRequests(ctx context.Context, repo string) ([]forge.ChangeRequestStatus, error) {
	var out []forge.ChangeRequestStatus
	q := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"100"}}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var batch []pull
		if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, p := range batch {
			if !strings.HasPrefix(p.Head.Ref, forge.AgentBranchPrefix) {
				continue
			}
			s := forge.ChangeRequestStatus{Number: p.Number, URL: p.HTMLURL, Head: p.Head.Ref, State: forge.ChangeOpen, CreatedAt: p.CreatedAt}
			switch {
			case p.MergedAt != nil:
				s.State, s.ClosedAt = forge.ChangeMerged, *p.MergedAt
			case p.State == "closed":
				s.State = forge.ChangeClosed
				if p.ClosedAt != nil {
					s.ClosedAt = *p.ClosedAt
				}
			}
			out = append(out, s)
			if len(out) == forge.MaxAgentChangeRequests {
				return out, nil
			}
		}
		// Au-delà de quelques pages, les PR sont trop anciennes pour une
		// semaine ou un palier.
		if len(batch) < 100 || page == 5 {
			return out, nil
		}
	}
}

// ChangeRequestCommits implémente forge.CodeForge ; GitHub rend au plus 250
// commits d'une PR.
func (c *Client) ChangeRequestCommits(ctx context.Context, repo string, number int) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var batch []struct {
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100&page=%d", repo, number, page), nil, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.Commit.Message)
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
}

// comments lit tous les commentaires d'un ticket, du plus ancien au plus
// récent.
func (c *Client) comments(ctx context.Context, repo string, number int) ([]forge.Comment, error) {
	var out []forge.Comment
	for page := 1; ; page++ {
		var batch []comment
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, number, page), nil, &batch); err != nil {
			return nil, err
		}
		for _, cm := range batch {
			out = append(out, forge.Comment{
				Author: cm.User.Login, Associated: associated(cm.Association), Body: cm.Body,
				CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt,
			})
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
}

// File implémente forge.CodeForge.
func (c *Client) File(ctx context.Context, repo, path string) ([]byte, bool, error) {
	req, err := c.request(ctx, http.MethodGet, "/repos/"+repo+"/contents/"+escapePath(path), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, c.scrub(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, c.statusError(req, resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, err == nil, err
}

// AddLabel implémente forge.TicketSource ; GitHub crée le label s'il
// n'existe pas.
func (c *Client) AddLabel(ctx context.Context, repo string, number int, label string) error {
	body := map[string][]string{"labels": {label}}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number), body, nil)
}

// RemoveLabel implémente forge.TicketSource.
func (c *Client) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", repo, number, url.PathEscape(label)), nil, nil)
	if se := (*statusErr)(nil); errors.As(err, &se) && se.code == http.StatusNotFound {
		return nil
	}
	return err
}

// Comment implémente forge.TicketSource.
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), map[string]string{"body": body}, nil)
}

// CreateIssue implémente forge.TicketSource.
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string) (int, string, error) {
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/issues", map[string]string{"title": title, "body": body}, &out); err != nil {
		return 0, "", err
	}
	return out.Number, out.HTMLURL, nil
}

// OpenChangeRequest implémente forge.CodeForge par une PR en brouillon ; ses
// labels sont posés ensuite, par l'API des issues.
func (c *Client) OpenChangeRequest(ctx context.Context, repo string, cr forge.ChangeRequest) (int, string, error) {
	body := map[string]any{"title": cr.Title, "head": cr.Head, "base": cr.Base, "body": cr.Body, "draft": true}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", body, &out); err != nil {
		return 0, "", err
	}
	for _, l := range cr.Labels {
		if err := c.AddLabel(ctx, repo, out.Number, l); err != nil {
			return out.Number, out.HTMLURL, err
		}
	}
	return out.Number, out.HTMLURL, nil
}

// CommentChangeRequest implémente forge.CodeForge : une PR se commente comme
// une issue.
func (c *Client) CommentChangeRequest(ctx context.Context, repo string, number int, body string) error {
	return c.Comment(ctx, repo, number, body)
}

// AddChangeRequestLabel implémente forge.CodeForge : une PR porte ses labels
// comme une issue.
func (c *Client) AddChangeRequestLabel(ctx context.Context, repo string, number int, label string) error {
	return c.AddLabel(ctx, repo, number, label)
}

// MaxExcerpt borne, en caractères, l'extrait rapporté pour un check.
const MaxExcerpt = 2000

// maxAnnotations borne les annotations lues par check en échec.
const maxAnnotations = 20

type checkRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	Output     struct {
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		Text        string `json:"text"`
		Annotations int    `json:"annotations_count"`
	} `json:"output"`
}

// Checks implémente forge.CodeForge : les check runs (GitHub Actions et autres
// applications) et les statuts de commit, plus les annotations d'un check
// run en échec. Le jeton doit pouvoir lire les checks et les statuts.
func (c *Client) Checks(ctx context.Context, repo, sha string) ([]forge.Check, error) {
	ref := url.PathEscape(sha)
	var runs struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+ref+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	var out []forge.Check
	for _, r := range runs.CheckRuns {
		ch := forge.Check{Name: r.Name, State: runState(r.Status, r.Conclusion), URL: r.HTMLURL}
		if ch.State == forge.CheckFailed {
			parts := []string{r.Output.Title, r.Output.Summary, r.Output.Text}
			if r.Output.Annotations > 0 {
				notes, err := c.annotations(ctx, repo, r.ID)
				if err != nil {
					return nil, err
				}
				parts = append(parts, notes)
			}
			ch.Excerpt = excerpt(parts...)
		}
		out = append(out, ch)
	}

	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+ref+"/status?per_page=100", nil, &combined); err != nil {
		return nil, err
	}
	for _, s := range combined.Statuses {
		ch := forge.Check{Name: s.Context, URL: s.TargetURL}
		switch s.State {
		case "success":
			ch.State = forge.CheckPassed
		case "failure", "error":
			ch.State, ch.Excerpt = forge.CheckFailed, excerpt(s.Description)
		default:
			ch.State = forge.CheckPending
		}
		out = append(out, ch)
	}
	return out, nil
}

// runState traduit l'état d'un check run ; une conclusion neutre ou un
// check sauté ne bloque pas.
func runState(status, conclusion string) forge.CheckState {
	if status != "completed" {
		return forge.CheckPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return forge.CheckPassed
	}
	return forge.CheckFailed
}

func (c *Client) annotations(ctx context.Context, repo string, id int64) (string, error) {
	var notes []struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		Message   string `json:"message"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/check-runs/%d/annotations?per_page=%d", repo, id, maxAnnotations), nil, &notes); err != nil {
		return "", err
	}
	var lines []string
	for _, n := range notes {
		lines = append(lines, fmt.Sprintf("%s:%d : %s", n.Path, n.StartLine, n.Message))
	}
	return strings.Join(lines, "\n"), nil
}

// excerpt assemble les parties non vides d'un rapport de check, tronquées à
// MaxExcerpt caractères.
func excerpt(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	r := []rune(strings.Join(kept, "\n\n"))
	if len(r) <= MaxExcerpt {
		return string(r)
	}
	return string(r[:MaxExcerpt]) + "…"
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return c.statusError(req, resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s : réponse illisible : %w", req.Method, req.URL.Path, err)
	}
	return nil
}

type statusErr struct {
	code int
	msg  string
}

func (e *statusErr) Error() string { return e.msg }

func (c *Client) statusError(req *http.Request, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &statusErr{
		code: resp.StatusCode,
		msg:  fmt.Sprintf("%s %s : HTTP %d : %s", req.Method, req.URL.Path, resp.StatusCode, c.redact(strings.TrimSpace(string(b)))),
	}
}

// scrub retire le jeton d'une erreur de transport, par précaution.
func (c *Client) scrub(err error) error {
	return fmt.Errorf("%s", c.redact(err.Error()))
}

func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[jeton masqué]")
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
