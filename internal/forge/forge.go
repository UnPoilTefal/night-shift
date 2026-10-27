// Package forge définit les deux contrats qu'une passe attend de ses outils
// (ADR 0012) : la source de tickets (tickets prêts, labels, commentaires,
// issue de digest) et la forge de code (fichiers du dépôt cible, accès git,
// demande de changement en brouillon, checks de CI). Chaque outil fournit
// son adaptateur ; la suite de contrat forgetest les juge tous.
package forge

import (
	"context"
	"regexp"
	"slices"
	"strconv"
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
	// OpenBlockers compte les tickets bloquants encore ouverts : les
	// dépendances natives de l'outil et, quand il n'en a pas partout, les
	// tickets que nomme une ligne « Blocked by » du corps (BlockedBy).
	OpenBlockers int
	// Comments sont les commentaires du ticket, du plus ancien au plus
	// récent, quel que soit leur auteur : la passe seule les filtre.
	Comments []Comment
}

// blockedByLine est une ligne « Blocked by: #n[, #n] » d'un corps de ticket.
var blockedByLine = regexp.MustCompile(`(?im)^[ \t]*blocked by:[ \t]*(#\d+(?:[ \t]*,[ \t]*#\d+)*)[ \t\r]*$`)

var ticketRef = regexp.MustCompile(`#(\d+)`)

// BlockedBy rend les numéros des tickets du même dépôt que nomment les lignes
// « Blocked by: #n[, #n] » d'un corps, sans doublon. C'est le repli du
// contrat pour une source de tickets dont les liens de blocage natifs
// manquent, ou manquent sur certaines éditions (ADR 0012) : son adaptateur
// compte ces tickets dans OpenBlockers tant qu'ils sont ouverts.
func BlockedBy(body string) []int {
	var out []int
	for _, line := range blockedByLine.FindAllStringSubmatch(body, -1) {
		for _, ref := range ticketRef.FindAllStringSubmatch(line[1], -1) {
			n, err := strconv.Atoi(ref[1])
			if err == nil && n > 0 && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
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

// ChangeRequestState est l'état d'une demande de changement.
type ChangeRequestState string

// États d'une demande de changement.
const (
	ChangeOpen   ChangeRequestState = "open"
	ChangeMerged ChangeRequestState = "merged"
	ChangeClosed ChangeRequestState = "closed"
)

// AgentBranchPrefix préfixe les branches que pousse la Publication :
// agent/<n>-<slug>, où n est le numéro du ticket.
const AgentBranchPrefix = "agent/"

// ChangeRequestStatus est l'état d'une demande de changement ouverte depuis
// une branche agent/.
type ChangeRequestStatus struct {
	Number int
	URL    string
	// Head est sa branche agent/.
	Head  string
	State ChangeRequestState
	// CreatedAt date son ouverture ; ClosedAt son merge ou sa fermeture,
	// zéro tant qu'elle est ouverte.
	CreatedAt, ClosedAt time.Time
}

// MaxAgentChangeRequests borne les demandes de changement que rend
// CodeForge.AgentChangeRequests.
const MaxAgentChangeRequests = 100

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
	// UpdatedTickets liste les tickets du dépôt, ouverts ou fermés, mis à
	// jour depuis since (commentaire, label, fermeture…), avec leurs labels
	// et leurs commentaires ; une demande de changement n'est jamais un
	// ticket. La liste peut déborder la période : l'appelant date ce qu'il
	// cherche par les commentaires.
	UpdatedTickets(ctx context.Context, repo string, since time.Time) ([]Ticket, error)
	// PutIssue ouvre l'issue title, qui porte label, ou met à jour le corps
	// de l'issue ouverte qui porte déjà ce label et ce titre ; le label est
	// créé s'il manque. Deux appels de même titre ne font qu'une issue.
	PutIssue(ctx context.Context, repo, label, title, body string) (number int, url string, err error)
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
	// AgentChangeRequests rend les demandes de changement du dépôt ouvertes
	// depuis une branche agent/, quel que soit leur état, de la plus récente
	// à la plus ancienne, au plus MaxAgentChangeRequests.
	AgentChangeRequests(ctx context.Context, repo string) ([]ChangeRequestStatus, error)
	// ChangeRequestCommits rend le message complet de chaque commit d'une
	// demande de changement, du plus ancien au plus récent : leurs trailers
	// disent qui les a écrits.
	ChangeRequestCommits(ctx context.Context, repo string, number int) ([]string, error)
}
