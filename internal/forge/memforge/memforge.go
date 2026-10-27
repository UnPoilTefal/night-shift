// Package memforge est le faux en mémoire d'une source de tickets et d'une
// forge de code, pour tester une passe sans réseau : on décrit l'état
// initial, on lance la passe, on lit l'état final.
package memforge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// Issue est l'état d'un ticket dans la forge en mémoire. Ticket.Comments
// porte les commentaires de l'état initial ; Comments, ceux que la passe a
// postés.
type Issue struct {
	forge.Ticket
	Closed   bool
	Comments []string

	// posted date les commentaires de Comments, signés de night-shift ;
	// updated est la dernière mise à jour du ticket.
	posted  []forge.Comment
	updated time.Time
}

// Change est l'état d'une demande de changement dans la forge en mémoire,
// avec ses labels (ChangeRequest.Labels) et les commentaires que la passe y a
// postés. State, ClosedAt et Commits se fixent par SetChange.
type Change struct {
	forge.ChangeRequest
	Number    int
	Comments  []string
	State     forge.ChangeRequestState
	CreatedAt time.Time
	ClosedAt  time.Time
	Commits   []string
}

// Bot signe les commentaires que la passe poste dans la forge en mémoire ;
// il est associé au dépôt, comme le compte du jeton.
const Bot = "night-shift"

// Forge implémente forge.TicketSource et forge.CodeForge en mémoire, sûre en
// accès concurrent.
type Forge struct {
	// Fail, s'il est renseigné, est appelé avant chaque opération avec son
	// nom ; une erreur rendue fait échouer l'opération (pannes simulées).
	Fail func(op string) error
	// GitURL est la base des adresses de clone que rend GitAccess : un
	// dossier de dépôts nus pour les tests.
	GitURL string
	// Now date les mises à jour et les commentaires (time.Now s'il est nul).
	Now func() time.Time

	mu      sync.Mutex
	files   map[string]map[string][]byte
	issues  map[string][]*Issue
	created map[string][]int
	changes map[string][]*Change
	checks  map[string][]forge.Check
	labels  map[string][]string
}

// New crée une forge vide.
func New() *Forge {
	return &Forge{
		files: map[string]map[string][]byte{}, issues: map[string][]*Issue{}, created: map[string][]int{},
		changes: map[string][]*Change{}, checks: map[string][]forge.Check{}, labels: map[string][]string{},
	}
}

// SetFile place un fichier sur la branche par défaut d'un dépôt.
func (f *Forge) SetFile(repo, path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[repo] == nil {
		f.files[repo] = map[string][]byte{}
	}
	f.files[repo][path] = []byte(content)
}

// AddIssue ajoute un ticket ; son dépôt est celui du ticket.
func (f *Forge) AddIssue(t forge.Ticket) {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated := t.CreatedAt
	for _, c := range t.Comments {
		updated = later(updated, later(c.CreatedAt, c.UpdatedAt))
	}
	f.issues[t.Repo] = append(f.issues[t.Repo], &Issue{Ticket: t, updated: updated})
	for _, l := range t.Labels {
		f.define(t.Repo, l)
	}
}

// CloseIssue ferme un ticket.
func (f *Forge) CloseIssue(repo string, number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.find(repo, number); i != nil {
		i.Closed = true
		i.updated = f.now()
	}
}

// SetChange fixe l'état d'une demande de changement, la date de son merge
// ou de sa fermeture, et les messages de ses commits.
func (f *Forge) SetChange(repo string, number int, state forge.ChangeRequestState, at time.Time, commits ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.findChange(repo, number); c != nil {
		c.State, c.ClosedAt, c.Commits = state, at, commits
	}
}

// RepoLabels rend les labels définis sur un dépôt, dans l'ordre de création.
func (f *Forge) RepoLabels(repo string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.labels[repo])
}

// Issue rend une copie de l'état d'un ticket.
func (f *Forge) Issue(repo string, number int) Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(repo, number)
	if i == nil {
		panic(fmt.Sprintf("memforge : ticket %s#%d inconnu", repo, number))
	}
	c := *i
	c.Labels = slices.Clone(i.Labels)
	c.Comments = slices.Clone(i.Comments)
	c.posted = slices.Clone(i.posted)
	return c
}

// ReadyTickets implémente forge.TicketSource.
func (f *Forge) ReadyTickets(ctx context.Context, repo string) ([]forge.Ticket, error) {
	if err := f.hook(ctx, "ReadyTickets"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var ts []forge.Ticket
	for _, i := range f.issues[repo] {
		if !i.Closed && slices.Contains(i.Labels, forge.LabelReady) {
			t := i.Ticket
			t.Labels = slices.Clone(i.Labels)
			t.Comments = slices.Clone(i.Ticket.Comments)
			ts = append(ts, t)
		}
	}
	return ts, nil
}

// File implémente forge.CodeForge.
func (f *Forge) File(ctx context.Context, repo, path string) ([]byte, bool, error) {
	if err := f.hook(ctx, "File"); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[repo][path]
	return slices.Clone(data), ok, nil
}

// AddLabel implémente forge.TicketSource.
func (f *Forge) AddLabel(ctx context.Context, repo string, number int, label string) error {
	if err := f.hook(ctx, "AddLabel"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(repo, number)
	if i == nil {
		return fmt.Errorf("ticket %s#%d inconnu", repo, number)
	}
	i.Labels = f.label(repo, i.Labels, label)
	i.updated = f.now()
	return nil
}

// RemoveLabel implémente forge.TicketSource.
func (f *Forge) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	if err := f.hook(ctx, "RemoveLabel"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(repo, number)
	if i == nil {
		return fmt.Errorf("ticket %s#%d inconnu", repo, number)
	}
	i.Labels = slices.DeleteFunc(i.Labels, func(l string) bool { return l == label })
	i.updated = f.now()
	return nil
}

// Comment implémente forge.TicketSource.
func (f *Forge) Comment(ctx context.Context, repo string, number int, body string) error {
	if err := f.hook(ctx, "Comment"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(repo, number)
	if i == nil {
		return fmt.Errorf("ticket %s#%d inconnu", repo, number)
	}
	now := f.now()
	i.Comments = append(i.Comments, body)
	i.posted = append(i.posted, forge.Comment{Author: Bot, Associated: true, Body: body, CreatedAt: now})
	i.updated = now
	return nil
}

// UpdatedTickets implémente forge.TicketSource : les commentaires rendus
// sont ceux de l'état initial, puis ceux que la passe a postés, signés de
// Bot.
func (f *Forge) UpdatedTickets(ctx context.Context, repo string, since time.Time) ([]forge.Ticket, error) {
	if err := f.hook(ctx, "UpdatedTickets"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var ts []forge.Ticket
	for _, i := range f.issues[repo] {
		if i.updated.Before(since) {
			continue
		}
		t := i.Ticket
		t.Labels = slices.Clone(i.Labels)
		t.Comments = append(slices.Clone(i.Ticket.Comments), i.posted...)
		ts = append(ts, t)
	}
	return ts, nil
}

// PutIssue implémente forge.TicketSource.
func (f *Forge) PutIssue(ctx context.Context, repo, label, title, body string) (int, string, error) {
	if err := f.hook(ctx, "PutIssue"); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.issues[repo] {
		if !i.Closed && i.Title == title && slices.Contains(i.Labels, label) {
			i.Body, i.updated = body, f.now()
			return i.Number, fmt.Sprintf("memforge://%s/issues/%d", repo, i.Number), nil
		}
	}
	n := f.next(repo)
	f.issues[repo] = append(f.issues[repo], &Issue{
		Ticket:  forge.Ticket{Repo: repo, Number: n, Title: title, Body: body, Author: Bot, AuthorAssociated: true, CreatedAt: f.now(), Labels: f.label(repo, nil, label)},
		updated: f.now(),
	})
	f.created[repo] = append(f.created[repo], n)
	return n, fmt.Sprintf("memforge://%s/issues/%d", repo, n), nil
}

// CreateIssue implémente forge.TicketSource ; l'URL rendue est
// memforge://<dépôt>/issues/<n>.
func (f *Forge) CreateIssue(ctx context.Context, repo, title, body string) (int, string, error) {
	if err := f.hook(ctx, "CreateIssue"); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.next(repo)
	f.issues[repo] = append(f.issues[repo], &Issue{Ticket: forge.Ticket{Repo: repo, Number: n, Title: title, Body: body}})
	f.created[repo] = append(f.created[repo], n)
	return n, fmt.Sprintf("memforge://%s/issues/%d", repo, n), nil
}

// CreatedIssues rend l'état des issues ouvertes par CreateIssue sur un
// dépôt, dans l'ordre de création.
func (f *Forge) CreatedIssues(repo string) []Issue {
	f.mu.Lock()
	numbers := slices.Clone(f.created[repo])
	f.mu.Unlock()
	out := make([]Issue, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, f.Issue(repo, n))
	}
	return out
}

// GitAccess implémente forge.CodeForge : <GitURL>/<dépôt>.git, sans
// utilisateur attendu avec le jeton.
func (f *Forge) GitAccess(repo string) forge.GitAccess {
	return forge.GitAccess{
		URL:      strings.TrimSuffix(f.GitURL, "/") + "/" + repo + ".git",
		Identity: forge.Identity{Name: "night-shift", Email: "night-shift@memforge.invalid"},
		Agent:    forge.Identity{Name: "night-shift agent", Email: "night-shift-agent@memforge.invalid"},
	}
}

// OpenChangeRequest implémente forge.CodeForge ; comme sur GitHub, tickets et
// demandes de changement partagent la même numérotation. L'URL rendue est
// memforge://<dépôt>/pull/<n>.
func (f *Forge) OpenChangeRequest(ctx context.Context, repo string, cr forge.ChangeRequest) (int, string, error) {
	if err := f.hook(ctx, "OpenChangeRequest"); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.next(repo)
	labels := cr.Labels
	cr.Labels = nil
	for _, l := range labels {
		cr.Labels = f.label(repo, cr.Labels, l)
	}
	f.changes[repo] = append(f.changes[repo], &Change{ChangeRequest: cr, Number: n, State: forge.ChangeOpen, CreatedAt: f.now()})
	return n, fmt.Sprintf("memforge://%s/pull/%d", repo, n), nil
}

// AgentChangeRequests implémente forge.CodeForge.
func (f *Forge) AgentChangeRequests(ctx context.Context, repo string) ([]forge.ChangeRequestStatus, error) {
	if err := f.hook(ctx, "AgentChangeRequests"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []forge.ChangeRequestStatus
	for _, c := range slices.Backward(f.changes[repo]) {
		if !strings.HasPrefix(c.Head, forge.AgentBranchPrefix) {
			continue
		}
		out = append(out, forge.ChangeRequestStatus{
			Number: c.Number, URL: fmt.Sprintf("memforge://%s/pull/%d", repo, c.Number), Head: c.Head,
			State: c.State, CreatedAt: c.CreatedAt, ClosedAt: c.ClosedAt,
		})
		if len(out) == forge.MaxAgentChangeRequests {
			break
		}
	}
	return out, nil
}

// ChangeRequestCommits implémente forge.CodeForge.
func (f *Forge) ChangeRequestCommits(ctx context.Context, repo string, number int) ([]string, error) {
	if err := f.hook(ctx, "ChangeRequestCommits"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.findChange(repo, number)
	if c == nil {
		return nil, fmt.Errorf("demande de changement %s#%d inconnue", repo, number)
	}
	return slices.Clone(c.Commits), nil
}

// CommentChangeRequest implémente forge.CodeForge.
func (f *Forge) CommentChangeRequest(ctx context.Context, repo string, number int, body string) error {
	if err := f.hook(ctx, "CommentChangeRequest"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.findChange(repo, number)
	if c == nil {
		return fmt.Errorf("demande de changement %s#%d inconnue", repo, number)
	}
	c.Comments = append(c.Comments, body)
	return nil
}

// AddChangeRequestLabel implémente forge.CodeForge.
func (f *Forge) AddChangeRequestLabel(ctx context.Context, repo string, number int, label string) error {
	if err := f.hook(ctx, "AddChangeRequestLabel"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.findChange(repo, number)
	if c == nil {
		return fmt.Errorf("demande de changement %s#%d inconnue", repo, number)
	}
	c.Labels = f.label(repo, c.Labels, label)
	return nil
}

// ChangeRequests rend les demandes de changement ouvertes sur un dépôt.
func (f *Forge) ChangeRequests(repo string) []forge.ChangeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]forge.ChangeRequest, 0, len(f.changes[repo]))
	for _, c := range f.changes[repo] {
		out = append(out, c.ChangeRequest)
	}
	return out
}

// Change rend une copie de l'état d'une demande de changement.
func (f *Forge) Change(repo string, number int) Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.findChange(repo, number)
	if c == nil {
		panic(fmt.Sprintf("memforge : demande de changement %s#%d inconnue", repo, number))
	}
	cp := *c
	cp.Labels = slices.Clone(c.Labels)
	cp.Comments = slices.Clone(c.Comments)
	return cp
}

// SetChecks fixe les checks de CI rapportés sur un commit.
func (f *Forge) SetChecks(repo, sha string, checks ...forge.Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks[repo+"@"+sha] = checks
}

// Checks implémente forge.CodeForge.
func (f *Forge) Checks(ctx context.Context, repo, sha string) ([]forge.Check, error) {
	if err := f.hook(ctx, "Checks"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.checks[repo+"@"+sha]), nil
}

// hook simule le comportement d'une vraie forge : un contexte annulé fait
// échouer l'appel, comme une panne injectée par Fail.
func (f *Forge) hook(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Fail != nil {
		return f.Fail(op)
	}
	return nil
}

// label pose label dans labels sans le doubler, et le définit sur le dépôt.
func (f *Forge) label(repo string, labels []string, label string) []string {
	f.define(repo, label)
	if slices.Contains(labels, label) {
		return labels
	}
	return append(labels, label)
}

func (f *Forge) define(repo, label string) {
	if !slices.Contains(f.labels[repo], label) {
		f.labels[repo] = append(f.labels[repo], label)
	}
}

// next rend le prochain numéro libre d'un dépôt, tickets et demandes de
// changement confondus.
func (f *Forge) next(repo string) int {
	n := 1
	for _, i := range f.issues[repo] {
		n = max(n, i.Number+1)
	}
	for _, c := range f.changes[repo] {
		n = max(n, c.Number+1)
	}
	return n
}

func (f *Forge) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (f *Forge) findChange(repo string, number int) *Change {
	for _, c := range f.changes[repo] {
		if c.Number == number {
			return c
		}
	}
	return nil
}

func (f *Forge) find(repo string, number int) *Issue {
	for _, i := range f.issues[repo] {
		if i.Number == number {
			return i
		}
	}
	return nil
}
