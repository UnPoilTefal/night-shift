// Package forge définit ce dont une passe a besoin d'une forge (GitHub,
// GitLab…) : lister les tickets prêts, lire un fichier du dépôt, poser des
// labels et commenter. Chaque forge fournit son adaptateur.
package forge

import (
	"context"
	"time"
)

// Labels du cycle de vie d'un ticket (docs/agents/triage-labels.md).
const (
	LabelReady      = "ready-for-agent"
	LabelInProgress = "agent-in-progress"
	LabelHuman      = "ready-for-human"
	LabelNeedsInfo  = "needs-info"
	LabelTriage     = "needs-triage"
)

// Ticket est une issue ouverte portant le rôle ready-for-agent.
type Ticket struct {
	Repo      string
	Number    int
	Title     string
	Body      string
	Author    string
	Labels    []string
	CreatedAt time.Time
	// OpenBlockers compte les dépendances natives encore ouvertes.
	OpenBlockers int
}

// Forge est le contrat qu'une passe attend d'une forge.
type Forge interface {
	// ReadyTickets liste les tickets ouverts portant LabelReady.
	ReadyTickets(ctx context.Context, repo string) ([]Ticket, error)
	// File lit un fichier sur la branche par défaut ; ok vaut false s'il
	// n'existe pas.
	File(ctx context.Context, repo, path string) (data []byte, ok bool, err error)
	// AddLabel pose un label sur un ticket.
	AddLabel(ctx context.Context, repo string, number int, label string) error
	// RemoveLabel retire un label ; l'absence du label n'est pas une erreur.
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	// Comment ajoute un commentaire à un ticket.
	Comment(ctx context.Context, repo string, number int, body string) error
}
