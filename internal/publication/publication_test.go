package publication_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/publication"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

// fixture rend un dépôt distant, la tâche réservée sur sa branche main et la
// série de commits qu'un agent aurait produite par-dessus.
func fixture(t *testing.T) (*testgit.Remote, harness.Task, [][]byte) {
	t.Helper()
	src := testgit.New(t)
	src.Write("README.md", "bonjour\n")
	src.Commit("init")
	remote := testgit.NewRemote(t, "o/a", src)
	base := src.Out("rev-parse", "HEAD")

	src.Write("f.go", "package f\n")
	src.Git("add", "-A")
	src.Git("-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "-q", "-m", "feat: f", "-m", "Le corps du message.")
	src.Write("g.go", "package f\n")
	src.Git("add", "-A")
	src.Git("-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "-q", "-m", "test: g")
	out := t.TempDir()
	src.Git("format-patch", "-q", "-o", out, base+"..HEAD")
	names, _ := filepath.Glob(filepath.Join(out, "*.patch"))
	slices.Sort(names)
	var patches [][]byte
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		patches = append(patches, b)
	}

	task := harness.Task{
		Ticket:     forge.Ticket{Repo: "o/a", Number: 7, Title: "Ajouter la fonction F !"},
		PassID:     "pass-42",
		BaseBranch: "main",
		BaseSHA:    base,
	}
	return remote, task, patches
}

func TestPublishPushesAgentBranchAndOpensDraft(t *testing.T) {
	remote, task, patches := fixture(t)
	f := memforge.New()
	p := publication.Git{Forge: f, GitURL: remote.Base, Token: "ghp_secret"}
	mainBefore := remote.Out("rev-parse", "main")

	url, err := p.Publish(context.Background(), task, harness.Result{Outcome: harness.Succeeded, Agent: "claude", Patches: patches})
	if err != nil {
		t.Fatal(err)
	}

	const branch = "agent/7-ajouter-la-fonction-f"
	log := remote.Out("log", "--format=%an|%s|%(trailers:only,unfold)", task.BaseSHA+".."+branch)
	for _, want := range []string{"agent|feat: f|", "agent|test: g|", "Night-Shift-Agent: claude", "Night-Shift-Pass: pass-42"} {
		if !strings.Contains(log, want) {
			t.Fatalf("historique de %s :\n%s\nattendu %q", branch, log, want)
		}
	}
	if n := strings.Count(log, "Night-Shift-Pass: pass-42"); n != 2 {
		t.Fatalf("trailer présent sur %d commits, attendu 2 :\n%s", n, log)
	}
	if body := remote.Out("log", "-1", "--format=%b", branch+"~1"); !strings.Contains(body, "Le corps du message.") {
		t.Fatalf("corps du message perdu : %q", body)
	}
	if got := remote.Out("rev-parse", "main"); got != mainBefore {
		t.Fatal("la branche main a bougé")
	}
	if tags := remote.Out("tag"); tags != "" {
		t.Fatalf("tags poussés : %q", tags)
	}
	prs := f.DraftPRs("o/a")
	if len(prs) != 1 || url == "" {
		t.Fatalf("PR ouvertes = %+v, url = %q", prs, url)
	}
	if pr := prs[0]; pr.Head != branch || pr.Base != "main" || pr.Title != task.Ticket.Title || !strings.Contains(pr.Body, "#7") || !strings.Contains(pr.Body, "pass-42") {
		t.Fatalf("PR = %+v", pr)
	}
}

func TestPublishRefusesASeriesThatDoesNotApply(t *testing.T) {
	remote, task, _ := fixture(t)
	f := memforge.New()
	p := publication.Git{Forge: f, GitURL: remote.Base}
	bogus := []byte("From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001\nFrom: agent <agent@example.com>\nSubject: [PATCH] x\n\n---\n README.md | 1 +\n\ndiff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n absent\n+ligne\n")

	_, err := p.Publish(context.Background(), task, harness.Result{Outcome: harness.Succeeded, Patches: [][]byte{bogus}})

	if err == nil {
		t.Fatal("erreur attendue")
	}
	if branches := remote.Out("branch", "--list", "agent/*"); branches != "" {
		t.Fatalf("branche poussée malgré l'échec : %q", branches)
	}
	if len(f.DraftPRs("o/a")) != 0 {
		t.Fatal("PR ouverte malgré l'échec")
	}
}

func TestBranchTransliteratesAccentsAndBoundsLength(t *testing.T) {
	for title, want := range map[string]string{
		"Démo : section Utilisation dans le README": "agent/2-demo-section-utilisation-dans-le-readme",
		"Œuvre à l'été, garçon !":                   "agent/2-oeuvre-a-l-ete-garcon",
		"日本語":                                       "agent/2",
		strings.Repeat("très long ", 10):            "agent/2-tres-long-tres-long-tres-long-tres-long",
	} {
		if got := publication.Branch(forge.Ticket{Number: 2, Title: title}); got != want {
			t.Errorf("Branch(%q) = %q, attendu %q", title, got, want)
		}
	}
}
