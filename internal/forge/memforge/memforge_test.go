package memforge_test

import (
	"slices"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/forgetest"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
)

func TestContract(t *testing.T) {
	forgetest.Run(t, func(*testing.T) forgetest.Fixture { return fixture{memforge.New()} })
}

// fixture branche la forge en mémoire sur la suite de contrat.
type fixture struct{ *memforge.Forge }

func (f fixture) Tickets() forge.TicketSource { return f.Forge }
func (f fixture) Code() forge.CodeForge       { return f.Forge }

func (f fixture) AddTicket(t forge.Ticket, closed bool) {
	f.AddIssue(t)
	if closed {
		f.CloseIssue(t.Repo, t.Number)
	}
}

func (f fixture) Ticket(repo string, number int) forgetest.Ticket {
	i := f.Issue(repo, number)
	var comments []string
	for _, c := range i.Ticket.Comments {
		comments = append(comments, c.Body)
	}
	return forgetest.Ticket{Title: i.Title, Body: i.Body, Labels: i.Labels, Comments: append(comments, i.Comments...)}
}

func (f fixture) ChangeRequest(repo string, number int) forgetest.ChangeRequest {
	c := f.Change(repo, number)
	return forgetest.ChangeRequest{ChangeRequest: c.ChangeRequest, Draft: true, Comments: slices.Clone(c.Comments)}
}
