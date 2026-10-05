// Package memforge est une forge en mémoire, pour tester une passe sans
// réseau : on décrit l'état initial, on lance la passe, on lit l'état final.
package memforge

import (
	"context"
	"fmt"
	"slices"
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

// Pull est l'état d'une PR dans la forge en mémoire, avec les commentaires
// que la passe y a postés.
type Pull struct {
	forge.PullRequest
	Number   int
	Comments []string
}

// Forge est une forge en mémoire, sûre en accès concurrent.
type Forge struct {
	// Fail, s'il est renseigné, est appelé avant chaque opération avec son
	// nom ; une erreur rendue fait échouer l'opération (pannes simulées).
	Fail func(op string) error

	mu      sync.Mutex
	files   map[string]map[string][]byte
	issues  map[string][]*Issue
	created map[string][]int
	pulls   map[string][]*Pull
	checks  map[string][]forge.Check
}

// New crée une forge vide.
func New() *Forge {
	return &Forge{files: map[string]map[string][]byte{}, issues: map[string][]*Issue{}, created: map[string][]int{}, pulls: map[string][]*Pull{}, checks: map[string][]forge.Check{}}
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

// ReadyTickets implémente forge.Forge.
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

// File implémente forge.Forge.
func (f *Forge) File(ctx context.Context, repo, path string) ([]byte, bool, error) {
	if err := f.hook(ctx, "File"); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[repo][path]
	return slices.Clone(data), ok, nil
}

// AddLabel implémente forge.Forge.
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
	if !slices.Contains(i.Labels, label) {
		i.Labels = append(i.Labels, label)
	}
	return nil
}

// RemoveLabel implémente forge.Forge.
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

// Comment implémente forge.Forge.
func (f *Forge) Comment(ctx context.Context, repo string, number int, body string) error {
	if err := f.hook(ctx, "Comment"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.find(repo, number); i != nil {
		i.Comments = append(i.Comments, body)
		return nil
	}
	if p := f.findPull(repo, number); p != nil {
		p.Comments = append(p.Comments, body)
		return nil
	}
	return fmt.Errorf("ticket ou PR %s#%d inconnu", repo, number)
}

// CreateIssue implémente forge.Forge ; l'URL rendue est
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

// OpenDraftPR implémente forge.Forge ; comme sur GitHub, issues et PR
// partagent la même numérotation. L'URL rendue est
// memforge://<dépôt>/pull/<n>.
func (f *Forge) OpenDraftPR(ctx context.Context, repo string, pr forge.PullRequest) (int, string, error) {
	if err := f.hook(ctx, "OpenDraftPR"); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.next(repo)
	f.pulls[repo] = append(f.pulls[repo], &Pull{PullRequest: pr, Number: n})
	return n, fmt.Sprintf("memforge://%s/pull/%d", repo, n), nil
}

// DraftPRs rend les PR en brouillon ouvertes sur un dépôt.
func (f *Forge) DraftPRs(repo string) []forge.PullRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]forge.PullRequest, 0, len(f.pulls[repo]))
	for _, p := range f.pulls[repo] {
		out = append(out, p.PullRequest)
	}
	return out
}

// Pull rend une copie de l'état d'une PR.
func (f *Forge) Pull(repo string, number int) Pull {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.findPull(repo, number)
	if p == nil {
		panic(fmt.Sprintf("memforge : PR %s#%d inconnue", repo, number))
	}
	c := *p
	c.Comments = slices.Clone(p.Comments)
	return c
}

// SetChecks fixe les checks de CI rapportés sur un commit.
func (f *Forge) SetChecks(repo, sha string, checks ...forge.Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks[repo+"@"+sha] = checks
}

// Checks implémente forge.Forge.
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

// next rend le prochain numéro libre d'un dépôt, issues et PR confondues.
func (f *Forge) next(repo string) int {
	n := 1
	for _, i := range f.issues[repo] {
		n = max(n, i.Number+1)
	}
	for _, p := range f.pulls[repo] {
		n = max(n, p.Number+1)
	}
	return n
}

func (f *Forge) findPull(repo string, number int) *Pull {
	for _, p := range f.pulls[repo] {
		if p.Number == number {
			return p
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
