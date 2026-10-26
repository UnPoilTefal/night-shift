// Package gitlab est l'adaptateur GitLab de la source de tickets et de la
// forge de code, par l'API REST v4 de gitlab.com ou d'une instance
// auto-hébergée, git en HTTPS et un jeton fourni à l'exécution. Un dépôt
// est désigné par le chemin complet de son projet, sous-groupes compris, et
// un ticket par l'iid de son issue.
//
// Le client HTTP vérifie toujours les certificats, avec le magasin standard
// du système : une instance servie par une autorité interne s'accepte en
// ajoutant cette autorité au magasin du conteneur, jamais en désactivant la
// vérification.
package gitlab

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
	"sync"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// DefaultBaseURL est l'URL de l'API REST de gitlab.com.
const DefaultBaseURL = "https://gitlab.com/api/v4"

// DefaultGitURL est la base des adresses de clone de gitlab.com.
const DefaultGitURL = "https://gitlab.com"

// ToolTokenEnv sont les variables où les outils GitLab (glab, CI) cherchent
// un jeton.
var ToolTokenEnv = []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "GL_TOKEN", "CI_JOB_TOKEN"}

// TokenPrefixes sont les préfixes par défaut des jetons GitLab : jetons
// d'accès personnels, de projet ou de groupe, de déploiement, de job CI, de
// déclenchement de pipeline, de runner, d'agent Kubernetes, de flux et
// d'e-mail entrant, et secrets d'application OAuth. Une instance peut
// changer le préfixe de ses jetons d'accès : le garde-fou ne le connaît pas.
var TokenPrefixes = []string{"glpat-", "gldt-", "glcbt-", "glptt-", "glrt-", "glagent-", "glft-", "glimt-", "gloas-"}

// Developer est le niveau d'accès minimal d'un auteur associé.
const Developer = 30

// pathSegment est un segment de chemin que GitLab accepte : il ne commence
// ni par un tiret ni par un point.
var pathSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ValidRepo vérifie qu'un dépôt est désigné comme GitLab l'attend : le
// chemin complet de son projet, groupe/projet ou groupe/sous-groupe/projet.
func ValidRepo(repo string) error {
	segments := strings.Split(repo, "/")
	valid := len(segments) >= 2 && !strings.Contains(repo, "..") &&
		!strings.HasSuffix(repo, ".git") && !strings.HasSuffix(repo, ".atom")
	for _, s := range segments {
		valid = valid && pathSegment.MatchString(s)
	}
	if !valid {
		return fmt.Errorf("projet %q invalide, attendu groupe/projet, sous-groupes compris", repo)
	}
	return nil
}

// draftPrefix marque une merge request en brouillon ; draftMarker reconnaît
// aussi les autres marques que GitLab accepte.
const draftPrefix = "Draft: "

var draftMarker = regexp.MustCompile(`(?i)^\s*(\[draft\]|\(draft\)|draft:)`)

// maxTitle est la longueur maximale, en caractères, d'un titre GitLab.
const maxTitle = 255

// Client implémente forge.TicketSource et forge.CodeForge pour GitLab.
type Client struct {
	baseURL string
	gitURL  string
	token   string
	http    *http.Client

	mu sync.Mutex
	// labels sont les labels dont l'existence est établie, par projet.
	labels map[string]bool
	// traces sont les fins de log des jobs en échec déjà lues : le log d'un
	// job terminé ne change plus, et un job relancé change d'identifiant.
	traces map[int64]string
}

// New crée un client sur l'API baseURL (jusqu'à /api/v4 compris) ; gitURL
// est la base des adresses de clone (DefaultGitURL, ou un dossier de dépôts
// nus pour les tests). Le jeton n'apparaît jamais dans une erreur.
func New(baseURL, gitURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		gitURL:  strings.TrimSuffix(gitURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		labels:  map[string]bool{},
		traces:  map[int64]string{},
	}
}

// GitAccess implémente forge.CodeForge. GitLab attend le jeton avec
// l'utilisateur oauth2. Les commits sont signés d'adresses du domaine
// users.noreply de l'instance, celui que GitLab réserve par défaut aux
// adresses privées : elles ne désignent aucun compte.
func (c *Client) GitAccess(repo string) forge.GitAccess {
	host := "localhost"
	if u, err := url.Parse(c.gitURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return forge.GitAccess{
		URL:      c.gitURL + "/" + repo + ".git",
		User:     "oauth2",
		Identity: forge.Identity{Name: "night-shift", Email: "night-shift@users.noreply." + host},
		Agent:    forge.Identity{Name: "night-shift agent", Email: "night-shift-agent@users.noreply." + host},
	}
}

// project rend le préfixe d'API d'un projet, désigné par son chemin complet
// encodé.
func project(repo string) string { return "/projects/" + url.PathEscape(repo) }

type user struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type issue struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	Author      user      `json:"author"`
	Labels      []string  `json:"labels"`
	WebURL      string    `json:"web_url"`
}

type note struct {
	Body      string    `json:"body"`
	Author    user      `json:"author"`
	System    bool      `json:"system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReadyTickets implémente forge.TicketSource. Une merge request n'est jamais
// une issue sur GitLab : rien n'est à écarter.
func (c *Client) ReadyTickets(ctx context.Context, repo string) ([]forge.Ticket, error) {
	members := map[int]bool{}
	var ts []forge.Ticket
	q := url.Values{"state": {"opened"}, "labels": {forge.LabelReady}, "scope": {"all"}, "per_page": {"100"}}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var batch []issue
		if err := c.do(ctx, http.MethodGet, project(repo)+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, i := range batch {
			associated, err := c.associated(ctx, repo, i.Author, members)
			if err != nil {
				return nil, err
			}
			t := forge.Ticket{
				Repo: repo, Number: i.IID, Title: i.Title, Body: i.Description,
				Author: i.Author.Username, AuthorAssociated: associated, Labels: i.Labels, CreatedAt: i.CreatedAt,
			}
			if t.OpenBlockers, err = c.openBlockers(ctx, repo, i); err != nil {
				return nil, err
			}
			if t.Comments, err = c.notes(ctx, repo, i.IID, members); err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		if len(batch) < 100 {
			return ts, nil
		}
	}
}

// associated dit si un compte est membre du projet, directement ou par un
// groupe parent, avec au moins le rôle Developer ; members garde les
// réponses le temps d'une lecture.
func (c *Client) associated(ctx context.Context, repo string, u user, members map[int]bool) (bool, error) {
	if ok, seen := members[u.ID]; seen {
		return ok, nil
	}
	var m struct {
		AccessLevel int `json:"access_level"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/members/all/%d", project(repo), u.ID), nil, &m)
	if notFound(err) {
		err = nil
	}
	if err != nil {
		return false, err
	}
	members[u.ID] = m.AccessLevel >= Developer
	return members[u.ID], nil
}

// notes lit les commentaires d'une issue, du plus ancien au plus récent ;
// les notes système (changement de label, mention…) n'en sont pas.
func (c *Client) notes(ctx context.Context, repo string, iid int, members map[int]bool) ([]forge.Comment, error) {
	var out []forge.Comment
	for page := 1; ; page++ {
		var batch []note
		path := fmt.Sprintf("%s/issues/%d/notes?sort=asc&order_by=created_at&per_page=100&page=%d", project(repo), iid, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, n := range batch {
			if n.System {
				continue
			}
			associated, err := c.associated(ctx, repo, n.Author, members)
			if err != nil {
				return nil, err
			}
			cm := forge.Comment{Author: n.Author.Username, Associated: associated, Body: n.Body, CreatedAt: n.CreatedAt}
			// GitLab date aussi une note jamais modifiée.
			if n.UpdatedAt.After(n.CreatedAt) {
				cm.UpdatedAt = n.UpdatedAt
			}
			out = append(out, cm)
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
}

// openBlockers compte les issues ouvertes qui bloquent i : ses liens « est
// bloquée par », que seules certaines éditions exposent, et les issues du
// projet que nomme une ligne « Blocked by » de son corps (forge.BlockedBy).
// Une issue nommée des deux façons compte une fois.
func (c *Client) openBlockers(ctx context.Context, repo string, i issue) (int, error) {
	open := map[string]bool{}
	var links []struct {
		issue
		LinkType string `json:"link_type"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/links", project(repo), i.IID), nil, &links)
	if err != nil && !notFound(err) {
		return 0, err
	}
	for _, l := range links {
		if l.LinkType == "is_blocked_by" && l.State == "opened" {
			open[l.WebURL] = true
		}
	}
	for _, n := range forge.BlockedBy(i.Description) {
		if n == i.IID {
			continue
		}
		var b issue
		err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d", project(repo), n), nil, &b)
		if notFound(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if b.State == "opened" {
			open[b.WebURL] = true
		}
	}
	return len(open), nil
}

// File implémente forge.CodeForge.
func (c *Client) File(ctx context.Context, repo, path string) ([]byte, bool, error) {
	req, err := c.request(ctx, http.MethodGet, project(repo)+"/repository/files/"+url.PathEscape(path)+"/raw", nil)
	if err != nil {
		return nil, false, err
	}
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

// AddLabel implémente forge.TicketSource.
func (c *Client) AddLabel(ctx context.Context, repo string, number int, label string) error {
	return c.addLabel(ctx, repo, "issues", number, label)
}

// RemoveLabel implémente forge.TicketSource.
func (c *Client) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("%s/issues/%d", project(repo), number), map[string]string{"remove_labels": label}, nil)
}

// addLabel pose un label sur une issue ou une merge request (kind), après
// l'avoir créé dans le projet s'il manque.
func (c *Client) addLabel(ctx context.Context, repo, kind string, number int, label string) error {
	if err := c.ensureLabel(ctx, repo, label); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("%s/%s/%d", project(repo), kind, number), map[string]string{"add_labels": label}, nil)
}

// ensureLabel crée un label dans le projet s'il n'existe pas : un label déjà
// défini, sur le projet ou l'un de ses groupes, n'est pas une erreur, et un
// label établi ne se redemande pas.
func (c *Client) ensureLabel(ctx context.Context, repo, label string) error {
	key := repo + "\x00" + label
	c.mu.Lock()
	known := c.labels[key]
	c.mu.Unlock()
	if known {
		return nil
	}
	err := c.do(ctx, http.MethodPost, project(repo)+"/labels", map[string]string{"name": label, "color": labelColor(label)}, nil)
	if se := (*statusErr)(nil); errors.As(err, &se) && se.code == http.StatusConflict {
		err = nil
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.labels[key] = true
	c.mu.Unlock()
	return nil
}

// labelColor rend la couleur d'un label que night-shift crée ; GitLab en
// exige une.
func labelColor(label string) string {
	switch label {
	case forge.LabelSensitiveZone:
		return "#dc143c"
	case forge.LabelHuman, forge.LabelNeedsInfo, forge.LabelTriage:
		return "#ed9121"
	}
	return "#6699cc"
}

// Comment implémente forge.TicketSource.
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/notes", project(repo), number), map[string]string{"body": body}, nil)
}

// CreateIssue implémente forge.TicketSource.
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string) (int, string, error) {
	var out issue
	if err := c.do(ctx, http.MethodPost, project(repo)+"/issues", map[string]string{"title": title, "description": body}, &out); err != nil {
		return 0, "", err
	}
	return out.IID, out.WebURL, nil
}

// OpenChangeRequest implémente forge.CodeForge par une merge request en
// brouillon : son titre porte le préfixe Draft:, et sa branche source n'est
// jamais supprimée au merge. Ses labels sont posés ensuite.
func (c *Client) OpenChangeRequest(ctx context.Context, repo string, cr forge.ChangeRequest) (int, string, error) {
	title := cr.Title
	if !draftMarker.MatchString(title) {
		title = draftPrefix + title
	}
	if r := []rune(title); len(r) > maxTitle {
		title = string(r[:maxTitle-1]) + "…"
	}
	body := map[string]any{
		"source_branch": cr.Head, "target_branch": cr.Base, "title": title, "description": cr.Body,
		"remove_source_branch": false,
	}
	var out issue
	if err := c.do(ctx, http.MethodPost, project(repo)+"/merge_requests", body, &out); err != nil {
		return 0, "", err
	}
	for _, l := range cr.Labels {
		if err := c.AddChangeRequestLabel(ctx, repo, out.IID, l); err != nil {
			return out.IID, out.WebURL, err
		}
	}
	return out.IID, out.WebURL, nil
}

// CommentChangeRequest implémente forge.CodeForge.
func (c *Client) CommentChangeRequest(ctx context.Context, repo string, number int, body string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("%s/merge_requests/%d/notes", project(repo), number), map[string]string{"body": body}, nil)
}

// AddChangeRequestLabel implémente forge.CodeForge.
func (c *Client) AddChangeRequestLabel(ctx context.Context, repo string, number int, label string) error {
	return c.addLabel(ctx, repo, "merge_requests", number, label)
}

// MaxExcerpt borne, en caractères, l'extrait rapporté pour un check.
const MaxExcerpt = 2000

// maxTrace borne, en octets, la fin de log lue avant nettoyage.
const maxTrace = 64 << 10

type job struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
	WebURL       string `json:"web_url"`
	// Downstream est la pipeline qu'un job de déclenchement a lancée.
	Downstream *pipeline `json:"downstream_pipeline"`
}

type pipeline struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// Checks implémente forge.CodeForge : un check par job de la pipeline la
// plus récente du commit, jobs de déclenchement de pipelines enfants ou
// aval compris. Une pipeline sans job, comme une configuration CI invalide,
// est rapportée comme un check à elle seule. Aucune pipeline, aucun check.
func (c *Client) Checks(ctx context.Context, repo, sha string) ([]forge.Check, error) {
	p, err := c.pipeline(ctx, repo, sha)
	if err != nil || p == nil {
		return nil, err
	}
	jobs, err := c.jobs(ctx, repo, p.ID, "jobs")
	if err != nil {
		return nil, err
	}
	bridges, err := c.jobs(ctx, repo, p.ID, "bridges")
	if err != nil {
		return nil, err
	}
	if len(jobs)+len(bridges) == 0 {
		ch := forge.Check{Name: "pipeline", State: jobState(p.Status, false), URL: p.WebURL}
		if ch.State == forge.CheckFailed {
			ch.Excerpt = "pipeline " + p.Status + " sans job : configuration CI invalide ?"
		}
		return []forge.Check{ch}, nil
	}
	var out []forge.Check
	for _, j := range append(jobs, bridges...) {
		out = append(out, forge.Check{Name: j.Name, State: jobState(j.Status, j.AllowFailure), URL: j.WebURL})
	}
	for i, j := range jobs {
		if out[i].State == forge.CheckFailed {
			out[i].Excerpt = c.trace(ctx, repo, j.ID)
		}
	}
	for i, b := range bridges {
		if ch := &out[len(jobs)+i]; ch.State == forge.CheckFailed && b.Downstream != nil {
			ch.Excerpt = fmt.Sprintf("pipeline aval %s : %s", b.Downstream.Status, b.Downstream.WebURL)
		}
	}
	return out, nil
}

// pipeline rend la pipeline la plus récente du commit sha. À défaut, c'est
// la pipeline de tête d'une merge request dont sha est la tête : une
// pipeline de résultats fusionnés porte le commit de fusion, pas sha.
func (c *Client) pipeline(ctx context.Context, repo, sha string) (*pipeline, error) {
	var ps []pipeline
	q := url.Values{"sha": {sha}, "order_by": {"id"}, "sort": {"desc"}, "per_page": {"1"}}
	if err := c.do(ctx, http.MethodGet, project(repo)+"/pipelines?"+q.Encode(), nil, &ps); err != nil {
		return nil, err
	}
	if len(ps) > 0 {
		return &ps[0], nil
	}
	var mrs []struct {
		IID int    `json:"iid"`
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodGet, project(repo)+"/repository/commits/"+url.PathEscape(sha)+"/merge_requests", nil, &mrs)
	if err != nil {
		return nil, err
	}
	for _, m := range mrs {
		if m.SHA != sha {
			continue
		}
		var mr struct {
			HeadPipeline *pipeline `json:"head_pipeline"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/merge_requests/%d", project(repo), m.IID), nil, &mr); err != nil {
			return nil, err
		}
		if mr.HeadPipeline != nil {
			return mr.HeadPipeline, nil
		}
	}
	return nil, nil
}

// jobs lit les jobs (kind « jobs ») ou les jobs de déclenchement (kind
// « bridges ») d'une pipeline.
func (c *Client) jobs(ctx context.Context, repo string, id int64, kind string) ([]job, error) {
	var out []job
	for page := 1; ; page++ {
		var batch []job
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/pipelines/%d/%s?per_page=100&page=%d", project(repo), id, kind, page), nil, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			return out, nil
		}
	}
}

// jobState traduit l'état d'un job ou d'une pipeline. Un job autorisé à
// échouer ne bloque pas ; un job manuel non plus, sauf s'il bloque la
// pipeline, qui attend alors un humain.
func jobState(status string, allowFailure bool) forge.CheckState {
	switch status {
	case "success", "skipped":
		return forge.CheckPassed
	case "failed", "canceled":
		if allowFailure {
			return forge.CheckPassed
		}
		return forge.CheckFailed
	case "manual":
		if allowFailure {
			return forge.CheckPassed
		}
	}
	return forge.CheckPending
}

// trace rend la fin du log d'un job, nettoyée de ses séquences de terminal
// et tronquée à MaxExcerpt caractères au début d'une ligne. Un log illisible
// ne cache pas l'échec du job : l'extrait le dit, et le log sera relu.
func (c *Client) trace(ctx context.Context, repo string, id int64) string {
	c.mu.Lock()
	cached, ok := c.traces[id]
	c.mu.Unlock()
	if ok {
		return cached
	}
	req, err := c.request(ctx, http.MethodGet, fmt.Sprintf("%s/jobs/%d/trace", project(repo), id), nil)
	if err != nil {
		return "log illisible"
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "log illisible : " + c.scrub(err).Error()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "log illisible : " + c.statusError(req, resp).Error()
	}
	var t tail
	if _, err := io.Copy(&t, resp.Body); err != nil {
		return "log illisible : " + c.scrub(err).Error()
	}
	excerpt := logTail(string(t.buf), t.cut)
	c.mu.Lock()
	c.traces[id] = excerpt
	c.mu.Unlock()
	return excerpt
}

// tail garde les maxTrace derniers octets écrits.
type tail struct {
	buf []byte
	cut bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - maxTrace; over > 0 {
		t.buf, t.cut = t.buf[over:], true
	}
	return len(p), nil
}

// ansi reconnaît les séquences d'échappement de terminal d'un log de job.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// logTail nettoie un log de job et en garde la fin. Une ligne réécrite par
// retour chariot, comme les marqueurs de section de GitLab, ne garde que
// son dernier état.
func logTail(log string, cut bool) string {
	lines := strings.Split(ansi.ReplaceAllString(log, ""), "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		lines[i] = l[strings.LastIndex(l, "\r")+1:]
	}
	if cut {
		// La première ligne d'une fin de log est entamée.
		lines = lines[1:]
	}
	text := strings.Trim(strings.Join(lines, "\n"), "\n")
	r := []rune(text)
	if len(r) <= MaxExcerpt && !cut {
		return text
	}
	// L'extrait tronqué garde la place de sa marque « …\n ».
	if len(r) > MaxExcerpt-2 {
		text = string(r[len(r)-(MaxExcerpt-2):])
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return "…\n" + text
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
	req.Header.Set("PRIVATE-TOKEN", c.token)
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
		return fmt.Errorf("%s %s : réponse illisible : %w", req.Method, req.URL.EscapedPath(), err)
	}
	return nil
}

type statusErr struct {
	code int
	msg  string
}

func (e *statusErr) Error() string { return e.msg }

func notFound(err error) bool {
	se := (*statusErr)(nil)
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

func (c *Client) statusError(req *http.Request, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &statusErr{
		code: resp.StatusCode,
		msg:  fmt.Sprintf("%s %s : HTTP %d : %s", req.Method, req.URL.EscapedPath(), resp.StatusCode, c.redact(strings.TrimSpace(string(b)))),
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
