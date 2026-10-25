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

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// Issue est l'état d'un ticket dans la forge en mémoire. Ticket.Comments
// porte les commentaires de l'état initial ; Comments, ceux que la passe a
// postés.
type Issue struct {
	forge.Ticket
	Closed   bool
	Comments []string
}

// Change est l'état d'une demande de changement dans la forge en mémoire,
// avec ses labels (ChangeRequest.Labels) et les commentaires que la passe y a
// postés.
type Change struct {
	forge.ChangeRequest
	Number   int
	Comments []string
}

// Forge implémente forge.TicketSource et forge.CodeForge en mémoire, sûre en
// accès concurrent.
type Forge struct {
	// Fail, s'il est renseigné, est appelé avant chaque opération avec son
	// nom ; une erreur rendue fait échouer l'opération (pannes simulées).
	Fail func(op string) error
	// GitURL est la base des adresses de clone que rend GitAccess : un
	// dossier de dépôts nus pour les tests.
	GitURL string

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
	f.issues[t.Repo] = append(f.issues[t.Repo], &Issue{Ticket: t})
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
	i.Comments = append(i.Comments, body)
	return nil
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
	f.changes[repo] = append(f.changes[repo], &Change{ChangeRequest: cr, Number: n})
	return n, fmt.Sprintf("memforge://%s/pull/%d", repo, n), nil
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
