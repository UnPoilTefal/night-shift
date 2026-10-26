// Package gitlabtest est une API REST v4 GitLab simulée, à état, qui sert les
// routes dont l'adaptateur gitlab se sert. Elle juge l'adaptateur dans la
// suite de contrat et porte les étapes de bout en bout dans les tests de la
// CLI. Comme GitLab, elle numérote issues et merge requests séparément, et
// distingue les membres directs d'un projet de ceux hérités d'un groupe ;
// plus stricte que GitLab, elle refuse de poser un label que le projet ne
// définit pas.
package gitlabtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Bot est le compte du jeton : ses notes sont celles de night-shift, et il
// est Developer de chaque projet, comme le compte d'un jeton d'accès de
// projet.
const Bot = "night-shift"

// Issue est l'état d'une issue.
type Issue struct {
	IID         int
	Title       string
	Description string
	Author      string
	Labels      []string
	Closed      bool
	CreatedAt   time.Time
	Notes       []Note
	// Links sont ses liens vers d'autres issues.
	Links []Link
}

// Note est une note d'issue ou de merge request ; une note système est
// écrite par GitLab lui-même.
type Note struct {
	Author               string
	Body                 string
	System               bool
	CreatedAt, UpdatedAt time.Time
}

// Link lie une issue à une autre, d'un projet quelconque. Type vaut
// relates_to, blocks ou is_blocked_by.
type Link struct {
	Type string
	Repo string
	IID  int
}

// MergeRequest est l'état d'une merge request.
type MergeRequest struct {
	IID                        int
	SourceBranch, TargetBranch string
	Title, Description         string
	Labels                     []string
	RemoveSourceBranch         bool
	Notes                      []Note
	// SHA est le commit de tête de la merge request.
	SHA string
}

// draftMarker est une marque de brouillon que GitLab reconnaît en tête de
// titre, sans égard à la casse.
var draftMarker = regexp.MustCompile(`(?i)^\s*(\[draft\]|\(draft\)|draft:)`)

// Draft dit si la merge request est un brouillon, ce que GitLab lit dans la
// marque en tête de son titre.
func (m MergeRequest) Draft() bool { return draftMarker.MatchString(m.Title) }

// Job est un job de pipeline ; Trace est son log. Un job de déclenchement
// (Downstream non vide) lance une pipeline enfant ou aval, dont Downstream
// est l'état.
type Job struct {
	Name, Status string
	AllowFailure bool
	WebURL       string
	Trace        string
	Downstream   string
}

type project struct {
	issues   []*Issue
	mrs      []*MergeRequest
	files    map[string]string
	labels   []string
	members  map[int]int
	group    map[int]int
	pipeline map[string]*pipeline
	// head est la pipeline de tête de chaque merge request, par iid.
	head map[int]*pipeline
}

type pipeline struct {
	id     int
	status string
	jobs   []int
}

// Server est l'API simulée. URL est la base de l'API, jusqu'à /api/v4, et
// Base celle de l'instance. Une requête hors de l'API est une requête git :
// elle est notée, puis refusée.
type Server struct {
	URL, Base string

	t     *testing.T
	token string

	mu        sync.Mutex
	projects  map[string]*project
	users     []string
	jobs      map[int]Job
	pipelines map[int]*pipeline
	ids       int
	calls     []string
	gitCalls  []string
	// fallback est la pipeline de tout commit qui n'en a pas d'autre.
	fallback *pipeline
}

// New démarre une API qui n'accepte que token.
func New(t *testing.T, token string) *Server {
	s := &Server{t: t, token: token, projects: map[string]*project{}, jobs: map[int]Job{}, pipelines: map[int]*pipeline{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.Base = srv.URL
	s.URL = srv.URL + "/api/v4"
	return s
}

func (s *Server) project(repo string) *project {
	p := s.projects[repo]
	if p == nil {
		p = &project{files: map[string]string{}, members: map[int]int{}, group: map[int]int{}, pipeline: map[string]*pipeline{}, head: map[int]*pipeline{}}
		s.projects[repo] = p
	}
	return p
}

func (s *Server) user(name string) int {
	if i := slices.Index(s.users, name); i >= 0 {
		return i + 1
	}
	s.users = append(s.users, name)
	return len(s.users)
}

func (s *Server) define(p *project, label string) {
	if !slices.Contains(p.labels, label) {
		p.labels = append(p.labels, label)
	}
}

// AddIssue ajoute une issue au projet repo ; ses labels y sont définis.
func (s *Server) AddIssue(repo string, i Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(repo)
	i.Labels, i.Notes, i.Links = slices.Clone(i.Labels), slices.Clone(i.Notes), slices.Clone(i.Links)
	p.issues = append(p.issues, &i)
	for _, l := range i.Labels {
		s.define(p, l)
	}
}

// AddMember fait de user un membre de repo au niveau d'accès level,
// directement ou par un groupe parent (inherited).
func (s *Server) AddMember(repo, user string, level int, inherited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(repo)
	if inherited {
		p.group[s.user(user)] = level
	} else {
		p.members[s.user(user)] = level
	}
}

// DefineLabel définit un label sur le projet repo.
func (s *Server) DefineLabel(repo, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.define(s.project(repo), label)
}

// SetFile place un fichier sur la branche par défaut du projet repo.
func (s *Server) SetFile(repo, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project(repo).files[path] = content
}

// SetPipeline fait de status, avec ses jobs, la pipeline la plus récente du
// commit sha ; repo vide la donne à tout commit sans pipeline propre.
func (s *Server) SetPipeline(repo, sha, status string, jobs ...Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.newPipeline(status, jobs)
	if repo == "" {
		s.fallback = p
		return
	}
	s.project(repo).pipeline[sha] = p
}

// SetMergeRequestPipeline fait de sha la tête de la merge request iid, et de
// status, avec ses jobs, sa pipeline de tête, celle d'une pipeline de
// résultats fusionnés : elle porte un commit de fusion, et la recherche par
// sha ne la trouve pas.
func (s *Server) SetMergeRequestPipeline(repo string, iid int, sha, status string, jobs ...Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(repo)
	m := find(p.mrs, iid)
	if m == nil {
		panic(fmt.Sprintf("merge request %s!%d inconnue", repo, iid))
	}
	m.SHA = sha
	p.head[iid] = s.newPipeline(status, jobs)
}

func (s *Server) newPipeline(status string, jobs []Job) *pipeline {
	s.ids++
	p := &pipeline{id: s.ids, status: status}
	s.pipelines[p.id] = p
	for _, j := range jobs {
		s.ids++
		s.jobs[s.ids] = j
		p.jobs = append(p.jobs, s.ids)
	}
	return p
}

// Issue rend l'état d'une issue.
func (s *Server) Issue(repo string, iid int) Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := find(s.project(repo).issues, iid)
	if i == nil {
		panic(fmt.Sprintf("issue %s#%d inconnue", repo, iid))
	}
	c := *i
	c.Labels, c.Notes = slices.Clone(i.Labels), slices.Clone(i.Notes)
	return c
}

// Issues rend les issues d'un projet.
func (s *Server) Issues(repo string) []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Issue
	for _, i := range s.project(repo).issues {
		out = append(out, *i)
	}
	return out
}

// MergeRequests rend les merge requests d'un projet.
func (s *Server) MergeRequests(repo string) []MergeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MergeRequest
	for _, m := range s.project(repo).mrs {
		c := *m
		c.Labels, c.Notes = slices.Clone(m.Labels), slices.Clone(m.Notes)
		out = append(out, c)
	}
	return out
}

// Labels rend les labels définis sur un projet.
func (s *Server) Labels(repo string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.project(repo).labels)
}

// GitCalls rend les requêtes git reçues, « utilisateur chemin ».
func (s *Server) GitCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.gitCalls)
}

// Calls rend les requêtes reçues, « MÉTHODE chemin » sans la base de l'API.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (i *Issue) iid() int        { return i.IID }
func (m *MergeRequest) iid() int { return m.IID }

func find[T interface{ iid() int }](items []T, iid int) T {
	for _, it := range items {
		if it.iid() == iid {
			return it
		}
	}
	var none T
	return none
}

func (s *Server) webURL(repo, kind string, iid int) string {
	return fmt.Sprintf("https://gitlab.example/%s/-/%s/%d", repo, kind, iid)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v4/") {
		user, _, _ := r.BasicAuth()
		s.mu.Lock()
		s.gitCalls = append(s.gitCalls, user+" "+r.URL.Path)
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("PRIVATE-TOKEN") != s.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "401 Unauthorized"}`))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	s.calls = append(s.calls, r.Method+" "+path)
	rest, ok := strings.CutPrefix(path, "/projects/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) < 2 {
		s.unexpected(w, r)
		return
	}
	repo, err := url.PathUnescape(parts[0])
	if err != nil || strings.Contains(parts[0], "/") {
		s.unexpected(w, r)
		return
	}
	p := s.project(repo)
	route := parts[1:]
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string { v, _ := body[k].(string); return v }
	n := func(i int) int {
		if len(route) <= i {
			return 0
		}
		v, _ := strconv.Atoi(route[i])
		return v
	}

	switch {
	case route[0] == "issues" && len(route) == 1 && r.Method == http.MethodGet:
		s.listIssues(w, r, repo, p)
	case route[0] == "issues" && len(route) == 1 && r.Method == http.MethodPost:
		i := &Issue{IID: nextIID(p.issues), Title: str("title"), Description: str("description"), Author: Bot, CreatedAt: time.Now()}
		p.issues = append(p.issues, i)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, s.issueJSON(repo, i))
	case route[0] == "issues" && len(route) == 2:
		i := find(p.issues, n(1))
		if i == nil {
			notFound(w)
			return
		}
		if r.Method == http.MethodPut {
			if !s.relabel(w, p, &i.Labels, str("add_labels"), str("remove_labels")) {
				return
			}
		}
		writeJSON(w, s.issueJSON(repo, i))
	case route[0] == "issues" && len(route) == 3 && route[2] == "notes":
		i := find(p.issues, n(1))
		if i == nil {
			notFound(w)
			return
		}
		s.notes(w, r, &i.Notes, str("body"))
	case route[0] == "issues" && len(route) == 3 && route[2] == "links" && r.Method == http.MethodGet:
		i := find(p.issues, n(1))
		if i == nil {
			notFound(w)
			return
		}
		out := []map[string]any{}
		for _, l := range i.Links {
			if target := find(s.project(l.Repo).issues, l.IID); target != nil {
				e := s.issueJSON(l.Repo, target)
				e["link_type"] = l.Type
				out = append(out, e)
			}
		}
		writeJSON(w, out)
	case route[0] == "members" && len(route) == 3 && route[1] == "all" && r.Method == http.MethodGet:
		id := n(2)
		level, ok := p.members[id]
		if g, inherited := p.group[id]; inherited && g > level {
			level, ok = g, true
		}
		if id > 0 && id <= len(s.users) && s.users[id-1] == Bot {
			level, ok = 30, true
		}
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"id": id, "username": s.users[id-1], "access_level": level})
	case route[0] == "labels" && len(route) == 1 && r.Method == http.MethodPost:
		name := str("name")
		if name == "" || str("color") == "" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"message": "name et color sont requis"})
			return
		}
		if slices.Contains(p.labels, name) {
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]string{"message": "Label already exists"})
			return
		}
		p.labels = append(p.labels, name)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"name": name})
	case route[0] == "repository" && len(route) == 4 && route[1] == "files" && route[3] == "raw" && r.Method == http.MethodGet:
		file, _ := url.PathUnescape(route[2])
		content, ok := p.files[file]
		if !ok {
			notFound(w)
			return
		}
		_, _ = w.Write([]byte(content))
	case route[0] == "merge_requests" && len(route) == 1 && r.Method == http.MethodPost:
		remove, _ := body["remove_source_branch"].(bool)
		m := &MergeRequest{
			IID: nextIID(p.mrs), SourceBranch: str("source_branch"), TargetBranch: str("target_branch"),
			Title: str("title"), Description: str("description"), RemoveSourceBranch: remove,
		}
		p.mrs = append(p.mrs, m)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"iid": m.IID, "web_url": s.webURL(repo, "merge_requests", m.IID), "draft": m.Draft()})
	case route[0] == "merge_requests" && len(route) == 2 && r.Method == http.MethodGet:
		m := find(p.mrs, n(1))
		if m == nil {
			notFound(w)
			return
		}
		e := map[string]any{"iid": m.IID, "sha": m.SHA, "head_pipeline": nil}
		if pl := p.head[m.IID]; pl != nil {
			e["head_pipeline"] = s.pipelineJSON(repo, pl)
		}
		writeJSON(w, e)
	case route[0] == "repository" && len(route) == 4 && route[1] == "commits" && route[3] == "merge_requests" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, m := range p.mrs {
			if m.SHA == route[2] {
				out = append(out, map[string]any{"iid": m.IID, "sha": m.SHA})
			}
		}
		writeJSON(w, out)
	case route[0] == "merge_requests" && len(route) == 2 && r.Method == http.MethodPut:
		m := find(p.mrs, n(1))
		if m == nil {
			notFound(w)
			return
		}
		if s.relabel(w, p, &m.Labels, str("add_labels"), str("remove_labels")) {
			writeJSON(w, map[string]any{"iid": m.IID, "labels": m.Labels})
		}
	case route[0] == "merge_requests" && len(route) == 3 && route[2] == "notes":
		m := find(p.mrs, n(1))
		if m == nil {
			notFound(w)
			return
		}
		s.notes(w, r, &m.Notes, str("body"))
	case route[0] == "pipelines" && len(route) == 1 && r.Method == http.MethodGet:
		out := []map[string]any{}
		pl := p.pipeline[r.URL.Query().Get("sha")]
		if pl == nil {
			pl = s.fallback
		}
		if pl != nil {
			out = append(out, s.pipelineJSON(repo, pl))
		}
		writeJSON(w, out)
	case route[0] == "pipelines" && len(route) == 3 && (route[2] == "jobs" || route[2] == "bridges") && r.Method == http.MethodGet:
		pl := s.pipelines[n(1)]
		if pl == nil {
			notFound(w)
			return
		}
		out := []map[string]any{}
		for _, id := range pl.jobs {
			j := s.jobs[id]
			if (j.Downstream != "") != (route[2] == "bridges") {
				continue
			}
			e := map[string]any{"id": id, "name": j.Name, "status": j.Status, "allow_failure": j.AllowFailure, "web_url": j.WebURL}
			if j.Downstream != "" {
				e["downstream_pipeline"] = map[string]any{"id": 1000 + id, "status": j.Downstream, "web_url": fmt.Sprintf("https://gitlab.example/%s/-/pipelines/%d", repo, 1000+id)}
			}
			out = append(out, e)
		}
		writeJSON(w, out)
	case route[0] == "jobs" && len(route) == 3 && route[2] == "trace" && r.Method == http.MethodGet:
		j, ok := s.jobs[n(1)]
		if !ok {
			notFound(w)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(j.Trace))
	default:
		s.unexpected(w, r)
	}
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, repo string, p *project) {
	q := r.URL.Query()
	out := []map[string]any{}
	if page, _ := strconv.Atoi(q.Get("page")); page <= 1 {
		for _, i := range p.issues {
			if q.Get("state") == "opened" && i.Closed {
				continue
			}
			if want := q.Get("labels"); want != "" && slices.ContainsFunc(strings.Split(want, ","), func(l string) bool { return !slices.Contains(i.Labels, l) }) {
				continue
			}
			out = append(out, s.issueJSON(repo, i))
		}
	}
	writeJSON(w, out)
}

func (s *Server) pipelineJSON(repo string, pl *pipeline) map[string]any {
	return map[string]any{"id": pl.id, "status": pl.status, "web_url": fmt.Sprintf("https://gitlab.example/%s/-/pipelines/%d", repo, pl.id)}
}

func (s *Server) issueJSON(repo string, i *Issue) map[string]any {
	state := "opened"
	if i.Closed {
		state = "closed"
	}
	labels := append([]string{}, i.Labels...)
	return map[string]any{
		"iid": i.IID, "title": i.Title, "description": i.Description, "state": state, "created_at": i.CreatedAt,
		"author": map[string]any{"id": s.user(i.Author), "username": i.Author}, "labels": labels,
		"web_url": s.webURL(repo, "issues", i.IID),
	}
}

// relabel ajoute et retire des labels, séparés par des virgules ; un label
// que le projet ne définit pas est refusé.
func (s *Server) relabel(w http.ResponseWriter, p *project, labels *[]string, add, remove string) bool {
	for _, l := range split(add) {
		if !slices.Contains(p.labels, l) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"message": "label inconnu : " + l})
			return false
		}
		if !slices.Contains(*labels, l) {
			*labels = append(*labels, l)
		}
	}
	for _, l := range split(remove) {
		*labels = slices.DeleteFunc(*labels, func(x string) bool { return x == l })
	}
	return true
}

func (s *Server) notes(w http.ResponseWriter, r *http.Request, notes *[]Note, body string) {
	switch r.Method {
	case http.MethodGet:
		out := []map[string]any{}
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page <= 1 {
			for _, n := range *notes {
				updated := n.UpdatedAt
				if updated.IsZero() {
					updated = n.CreatedAt
				}
				out = append(out, map[string]any{
					"body": n.Body, "system": n.System, "created_at": n.CreatedAt, "updated_at": updated,
					"author": map[string]any{"id": s.user(n.Author), "username": n.Author},
				})
			}
		}
		writeJSON(w, out)
	case http.MethodPost:
		*notes = append(*notes, Note{Author: Bot, Body: body, CreatedAt: time.Now()})
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"body": body})
	default:
		s.unexpected(w, r)
	}
}

func (s *Server) unexpected(w http.ResponseWriter, r *http.Request) {
	s.t.Errorf("requête inattendue : %s %s", r.Method, r.URL)
	notFound(w)
}

func nextIID[T interface{ iid() int }](items []T) int {
	next := 1
	for _, it := range items {
		next = max(next, it.iid()+1)
	}
	return next
}

func split(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	writeJSON(w, map[string]string{"message": "404 Not found"})
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
