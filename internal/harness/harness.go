// Package harness définit le contrat de l'adaptateur qui fait travailler un
// agent sur un ticket. La passe ne connaît l'agent qu'à travers lui.
package harness

import (
	"context"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

// Outcome est l'issue d'un passage de l'agent sur un ticket.
type Outcome string

// Issues possibles.
const (
	// Succeeded : l'agent a produit un travail à publier.
	Succeeded Outcome = "succeeded"
	// Stopped : l'agent s'est arrêté de lui-même, avec un motif.
	Stopped Outcome = "stopped"
	// Failed : l'agent a échoué ou a été interrompu.
	Failed Outcome = "failed"
)

// Result est le compte rendu structuré d'un passage de l'agent.
type Result struct {
	Outcome Outcome
	Reason  string
}

// Harness fait travailler un agent sur un ticket.
type Harness interface {
	Run(ctx context.Context, t forge.Ticket) (Result, error)
}

// Stub est le harness factice du premier palier : il rend toujours un arrêt
// motivé, sans lancer d'agent.
type Stub struct{}

// Run implémente Harness.
func (Stub) Run(context.Context, forge.Ticket) (Result, error) {
	return Result{Outcome: Stopped, Reason: "harness factice : aucun agent n'est encore branché"}, nil
}
