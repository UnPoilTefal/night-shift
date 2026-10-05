// Package harness définit le contrat de l'adaptateur qui fait travailler un
// agent sur un ticket. La passe ne connaît l'agent qu'à travers lui.
package harness

import (
	"context"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// Outcome est l'issue d'un passage de l'agent sur un ticket.
type Outcome string

// Issues possibles.
const (
	// Succeeded : l'agent a produit une série de commits à publier.
	Succeeded Outcome = "succeeded"
	// NeedsInfo : l'agent s'est arrêté parce que le brief ne suffit pas,
	// qu'une précondition n'est pas remplie ou qu'il faudrait une dépendance
	// que le brief ne nomme pas.
	NeedsInfo Outcome = "needs-info"
	// Stopped : l'agent s'est arrêté de lui-même pour un autre motif.
	Stopped Outcome = "stopped"
	// Failed : l'agent a échoué ou a été interrompu.
	Failed Outcome = "failed"
)

// Task est ce que la passe confie à l'agent : le ticket réservé et le brief
// construit par la passe, seul contenu du ticket que l'agent reçoit.
type Task struct {
	Ticket forge.Ticket
	Brief  string
	PassID string
	// BaseBranch et BaseSHA désignent le commit du dépôt cible sur lequel
	// l'agent travaille ; la Publication y applique sa série de commits.
	BaseBranch, BaseSHA string
	// Round numérote le passage de l'agent sur le ticket, à partir de 1. À
	// partir du deuxième, l'agent repart de la PR publiée au tour précédent :
	// BaseSHA est la tête de sa branche et CIFailures les checks qui y
	// échouent.
	Round       int
	PullRequest *forge.Draft
	CIFailures  []forge.Check
	// PriorCostUSD et PriorDuration cumulent les tours précédents.
	PriorCostUSD  float64
	PriorDuration time.Duration
}

// Result est le compte rendu structuré d'un passage de l'agent.
type Result struct {
	Outcome Outcome
	Reason  string
	// Patches est la série de commits de l'agent, un patch format-patch par
	// commit, dans l'ordre ; elle n'est renseignée qu'en cas de succès.
	Patches [][]byte
	// Agent identifie l'agent dans le trailer des commits publiés.
	Agent      string
	CostUSD    float64
	Duration   time.Duration
	SessionID  string
	Transcript string
}

// Harness fait travailler un agent sur un ticket.
type Harness interface {
	Run(ctx context.Context, task Task) (Result, error)
}

// Stub est le harness factice : il rend toujours un arrêt motivé, sans
// lancer d'agent.
type Stub struct{}

// Run implémente Harness.
func (Stub) Run(context.Context, Task) (Result, error) {
	return Result{Outcome: Stopped, Reason: "harness factice : aucun agent n'est encore branché"}, nil
}
