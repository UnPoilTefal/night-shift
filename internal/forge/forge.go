// Package forge définit ce dont une passe a besoin d'une forge (GitHub,
// GitLab…) : lister les tickets prêts, lire un fichier du dépôt, poser des
// labels, commenter, ouvrir une issue de digest et une PR en brouillon, suivre
// la CI d'un commit. Chaque forge fournit son adaptateur.
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
	Repo   string
	Number int
	Title  string
	Body   string
	Author string
	// AuthorAssociated dit si l'auteur est associé au dépôt (propriétaire,
	// membre, collaborateur) ; un auteur associé est de confiance.
	AuthorAssociated bool
	Labels           []string
	CreatedAt        time.Time
	// OpenBlockers compte les dépendances natives encore ouvertes.
	OpenBlockers int
	// Comments sont les commentaires du ticket, du plus ancien au plus
	// récent, quel que soit leur auteur : la passe seule les filtre.
	Comments []Comment
}

// Comment est un commentaire d'un ticket.
type Comment struct {
	Author string
	// Associated dit si l'auteur est associé au dépôt, comme
	// Ticket.AuthorAssociated.
	Associated bool
	Body       string
	CreatedAt  time.Time
	// UpdatedAt est la date de la dernière modification, s'il y en a eu.
	UpdatedAt time.Time
}

// PullRequest décrit une PR à ouvrir, toujours en brouillon.
type PullRequest struct {
	// Head est la branche poussée, Base la branche visée.
	Head, Base  string
	Title, Body string
}

// Draft désigne une PR en brouillon ouverte par la Publication.
type Draft struct {
	Number int
	URL    string
	// Branch est la branche agent/ de la PR.
	Branch string
}

// CheckState est l'état d'un check de CI.
type CheckState string

// États d'un check.
const (
	CheckPending CheckState = "pending"
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
)

// Check est un check de CI rapporté sur un commit.
type Check struct {
	Name  string
	State CheckState
	// Excerpt est un extrait tronqué de ce que le check rapporte (titre,
	// résumé, annotations) ; URL renvoie vers son détail.
	Excerpt string
	URL     string
}

// Forge est le contrat qu'une passe attend d'une forge.
type Forge interface {
	// ReadyTickets liste les tickets ouverts portant LabelReady, avec leurs
	// commentaires.
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
	// CreateIssue ouvre une issue et rend son numéro et son URL.
	CreateIssue(ctx context.Context, repo, title, body string) (number int, url string, err error)
	// OpenDraftPR ouvre une PR en brouillon et rend son numéro et son URL.
	// Aucune méthode ne permet de la passer en prête, de la merger ni de
	// l'approuver. Comment commente une PR par son numéro.
	OpenDraftPR(ctx context.Context, repo string, pr PullRequest) (number int, url string, err error)
	// Checks rend les checks de CI rapportés sur un commit, quel que soit
	// leur état ; aucun check n'est encore rapporté juste après un push.
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
}
