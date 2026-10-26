package gitlab_test

import (
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/forgetest"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab/gitlabtest"
)

const token = "glpat-test-secret-token"

func TestContract(t *testing.T) {
	forgetest.Run(t, func(t *testing.T) forgetest.Fixture { return newFixture(t) })
}

// fixture présente l'API simulée sous les deux contrats : un auteur associé
// y est membre Developer du projet, et chaque bloqueur une issue ouverte
// d'un autre projet, liée par « est bloquée par ».
type fixture struct {
	srv    *gitlabtest.Server
	client *gitlab.Client
}

func newFixture(t *testing.T) *fixture {
	srv := gitlabtest.New(t, token)
	return &fixture{srv: srv, client: gitlab.New(srv.URL, "https://gitlab.example", token)}
}

func (f *fixture) Tickets() forge.TicketSource { return f.client }
func (f *fixture) Code() forge.CodeForge       { return f.client }

func (f *fixture) AddTicket(t forge.Ticket, closed bool) {
	i := gitlabtest.Issue{
		IID: t.Number, Title: t.Title, Description: t.Body, Author: t.Author,
		Labels: t.Labels, Closed: closed, CreatedAt: t.CreatedAt,
	}
	if t.AuthorAssociated {
		f.srv.AddMember(t.Repo, t.Author, gitlab.Developer, false)
	}
	for _, c := range t.Comments {
		i.Notes = append(i.Notes, gitlabtest.Note{Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
		if c.Associated {
			f.srv.AddMember(t.Repo, c.Author, gitlab.Developer, false)
		}
	}
	for n := range t.OpenBlockers {
		blockers := "blockers/" + t.Repo
		f.srv.AddIssue(blockers, gitlabtest.Issue{IID: 100*t.Number + n, Title: "bloqueur", Author: t.Author})
		i.Links = append(i.Links, gitlabtest.Link{Type: "is_blocked_by", Repo: blockers, IID: 100*t.Number + n})
	}
	f.srv.AddIssue(t.Repo, i)
}

func (f *fixture) SetFile(repo, path, content string) { f.srv.SetFile(repo, path, content) }

func (f *fixture) SetChecks(repo, sha string, checks ...forge.Check) {
	status := "success"
	var jobs []gitlabtest.Job
	for _, c := range checks {
		j := gitlabtest.Job{Name: c.Name, WebURL: c.URL, Trace: c.Excerpt}
		switch c.State {
		case forge.CheckPassed:
			j.Status = "success"
		case forge.CheckFailed:
			j.Status, status = "failed", "failed"
		default:
			j.Status = "running"
			if status == "success" {
				status = "running"
			}
		}
		jobs = append(jobs, j)
	}
	f.srv.SetPipeline(repo, sha, status, jobs...)
}

func (f *fixture) Ticket(repo string, number int) forgetest.Ticket {
	i := f.srv.Issue(repo, number)
	return forgetest.Ticket{Title: i.Title, Body: i.Description, Labels: i.Labels, Comments: bodies(i.Notes)}
}

func (f *fixture) ChangeRequest(repo string, number int) forgetest.ChangeRequest {
	for _, m := range f.srv.MergeRequests(repo) {
		if m.IID == number {
			return forgetest.ChangeRequest{
				ChangeRequest: forge.ChangeRequest{
					Head: m.SourceBranch, Base: m.TargetBranch, Title: strings.TrimPrefix(m.Title, "Draft: "),
					Body: m.Description, Labels: m.Labels,
				},
				Draft:    m.Draft(),
				Comments: bodies(m.Notes),
			}
		}
	}
	panic("merge request inconnue")
}

func (f *fixture) RepoLabels(repo string) []string { return f.srv.Labels(repo) }

// bodies rend le corps des notes qui ne sont pas des notes système.
func bodies(notes []gitlabtest.Note) []string {
	var out []string
	for _, n := range notes {
		if !n.System {
			out = append(out, n.Body)
		}
	}
	return out
}
