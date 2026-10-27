package cli_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab/gitlabtest"
	"github.com/UnPoilTefal/night-shift/internal/weekly"
)

func TestWeeklyReadsTheOutcomeOnTheForgeAndUpdatesOneIssue(t *testing.T) {
	w := newGitLabWorld(t)
	w.pass(t, "commit")
	mrs := w.gl.MergeRequests(glRepo)
	if len(mrs) != 1 {
		t.Fatalf("merge requests = %d, attendu celle de la passe", len(mrs))
	}
	// Un humain merge la demande telle que l'agent l'a écrite.
	w.gl.SetMergeRequestState(glRepo, mrs[0].IID, "merged", time.Now(), "feat: x\n\nNight-Shift-Agent: claude", "test: y\n\nNight-Shift-Agent: claude")
	t.Setenv(cli.TokenEnv, "")
	t.Setenv(cli.GitLabTokenEnv, glSecret)

	var outs []string
	for range 2 {
		code, stdout, stderr := run("weekly", "--forge", "gitlab", "--api-url", w.gl.URL, "--repo", glRepo, "--repo", "g/sans-adhesion")
		if code != 0 {
			t.Fatalf("weekly : code %d\n%s\n%s", code, stdout, stderr)
		}
		outs = append(outs, stdout)
	}

	var weeklies []gitlabtest.Issue
	for _, i := range w.gl.Issues(glRepo) {
		if slices.Contains(i.Labels, weekly.Label) {
			weeklies = append(weeklies, i)
		}
	}
	if len(weeklies) != 1 {
		t.Fatalf("issues hebdomadaires = %d, attendu une seule pour deux exécutions\n%s", len(weeklies), strings.Join(outs, "\n"))
	}
	from, _ := weekly.Week(time.Now())
	if i := weeklies[0]; i.Title != weekly.Title(from) || !strings.Contains(i.Description, "| #7 | mergé tel quel |") || !strings.Contains(i.Description, "1/1 (100 %)") {
		t.Fatalf("issue = %q\n%s", i.Title, i.Description)
	}
	if !strings.Contains(outs[1], "1/1 mergés") || !strings.Contains(outs[1], "g/sans-adhesion : aucune adhésion") {
		t.Fatalf("sortie = %q", outs[1])
	}
	if len(w.gl.Issues("g/sans-adhesion")) != 0 {
		t.Fatal("un dépôt sans adhésion a reçu une issue")
	}
}

func TestWeeklyRejectsABadWeek(t *testing.T) {
	t.Setenv(cli.TokenEnv, secret)
	if code, _, stderr := run("weekly", "--repo", "o/a", "--week", "semaine 43"); code != 2 || !strings.Contains(stderr, "AAAA-MM-JJ") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}
