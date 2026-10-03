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

// Issue est l'état d'un ticket dans la forge en mémoire.
type Issue struct {
	forge.Ticket
	Closed   bool
	Comments []string
}

// Forge est une forge en mémoire, sûre en accès concurrent.
type Forge struct {
	mu     sync.Mutex
	files  map[string]map[string][]byte
	issues map[string][]*Issue
}

// New crée une forge vide.
func New() *Forge {
	return &Forge{files: map[string]map[string][]byte{}, issues: map[string][]*Issue{}}
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
func (f *Forge) ReadyTickets(_ context.Context, repo string) ([]forge.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ts []forge.Ticket
	for _, i := range f.issues[repo] {
		if !i.Closed && slices.Contains(i.Labels, forge.LabelReady) {
			t := i.Ticket
			t.Labels = slices.Clone(i.Labels)
			ts = append(ts, t)
		}
	}
	return ts, nil
}

// File implémente forge.Forge.
func (f *Forge) File(_ context.Context, repo, path string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[repo][path]
	return slices.Clone(data), ok, nil
}

// AddLabel implémente forge.Forge.
func (f *Forge) AddLabel(_ context.Context, repo string, number int, label string) error {
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
func (f *Forge) RemoveLabel(_ context.Context, repo string, number int, label string) error {
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
func (f *Forge) Comment(_ context.Context, repo string, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(repo, number)
	if i == nil {
		return fmt.Errorf("ticket %s#%d inconnu", repo, number)
	}
	i.Comments = append(i.Comments, body)
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
