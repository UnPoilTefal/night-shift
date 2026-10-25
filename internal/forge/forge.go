// Package forge définit les deux contrats qu'une passe attend de ses outils
// (ADR 0012) : la source de tickets (tickets prêts, labels, commentaires,
// issue de digest) et la forge de code (fichiers du dépôt cible, accès git,
// demande de changement en brouillon, checks de CI). Chaque outil fournit
// son adaptateur ; la suite de contrat forgetest les juge tous.
package forge

import (
	"context"
	"strings"
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

// LabelSensitiveZone signale une demande de changement d'agent qui touche une zone sensible :
// un humain décide (ADR 0011).
const LabelSensitiveZone = "sensitive-zone"

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

// ChangeRequest décrit une demande de changement à ouvrir, toujours en
// brouillon.
type ChangeRequest struct {
	// Head est la branche poussée, Base la branche visée.
	Head, Base  string
	Title, Body string
	// Labels sont posés sur la demande dès son ouverture.
	Labels []string
}

// Draft désigne une demande de changement en brouillon ouverte par la
// Publication.
type Draft struct {
	Number int
	URL    string
	// Branch est la branche agent/ de la demande.
	Branch string
}

// Identity signe des commits.
type Identity struct {
	Name, Email string
}

// GitAccess est ce dont le transport git a besoin pour cloner un dépôt cible
// et y pousser.
type GitAccess struct {
	// URL est l'adresse de clone du dépôt.
	URL string
	// User est l'utilisateur que la forge attend avec le jeton.
	User string
	// Identity signe les commits que night-shift crée lui-même ; Agent, ceux
	// de l'agent.
	Identity, Agent Identity
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

// Label rend le nom du check sur une seule ligne.
func (c Check) Label() string { return strings.Join(strings.Fields(c.Name), " ") }

// Quote rend l'extrait du check en bloc de code Markdown, chaque ligne
// précédée de indent (au moins quatre espaces) : quel que soit son contenu,
// l'extrait ne peut pas sortir du bloc.
func (c Check) Quote(indent string) string {
	lines := strings.Split(strings.TrimRight(c.Excerpt, "\n"), "\n")
	for i, l := range lines {
		lines[i] = indent + l
	}
	return strings.Join(lines, "\n")
}

// TicketSource est le contrat qu'une passe attend d'une source de tickets.
type TicketSource interface {
	// ReadyTickets liste les tickets ouverts portant LabelReady, avec leurs
	// commentaires ; une demande de changement n'est jamais un ticket.
	ReadyTickets(ctx context.Context, repo string) ([]Ticket, error)
	// AddLabel pose un label sur un ticket, et crée le label s'il n'existe
	// pas ; le poser deux fois ne le double pas.
	AddLabel(ctx context.Context, repo string, number int, label string) error
	// RemoveLabel retire un label d'un ticket ; l'absence du label n'est pas
	// une erreur.
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	// Comment ajoute un commentaire à un ticket.
	Comment(ctx context.Context, repo string, number int, body string) error
	// CreateIssue ouvre une issue et rend son numéro et son URL.
	CreateIssue(ctx context.Context, repo, title, body string) (number int, url string, err error)
}

// CodeForge est le contrat qu'une passe attend de la forge de code qui
// héberge le dépôt cible. Aucune méthode ne permet de passer une demande de
// changement en prête, de la merger ni de l'approuver.
type CodeForge interface {
	// File lit un fichier sur la branche par défaut ; ok vaut false s'il
	// n'existe pas.
	File(ctx context.Context, repo, path string) (data []byte, ok bool, err error)
	// GitAccess rend de quoi cloner le dépôt et y pousser.
	GitAccess(repo string) GitAccess
	// OpenChangeRequest ouvre une demande de changement en brouillon, avec
	// ses labels, et rend son numéro et son URL. Si la pose d'un label
	// échoue, la demande ouverte est rendue avec l'erreur.
	OpenChangeRequest(ctx context.Context, repo string, cr ChangeRequest) (number int, url string, err error)
	// CommentChangeRequest ajoute un commentaire à une demande de changement.
	CommentChangeRequest(ctx context.Context, repo string, number int, body string) error
	// AddChangeRequestLabel pose un label sur une demande de changement déjà
	// ouverte, comme TicketSource.AddLabel sur un ticket.
	AddChangeRequestLabel(ctx context.Context, repo string, number int, label string) error
	// Checks rend les checks de CI rapportés sur un commit, quel que soit
	// leur état ; aucun check n'est encore rapporté juste après un push.
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
}
