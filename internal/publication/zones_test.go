package publication_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/memforge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/publication"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

// optIn est l'adhésion des dépôts de test : .github/** interdite, deploy/**
// sensible.
const optIn = `version: 1
trustLevel:
  ticketsPerPass: 1
  pullRequests: draft
toolImage: ghcr.io/example/tools-go:1
forbiddenZones:
  - ".github/**"
sensitiveZones:
  - path: "deploy/**"
`

// zoneRepo rend un dépôt de travail dont main porte l'adhésion et des
// fichiers dans chaque zone, le dépôt distant qui le sert et la tâche
// réservée sur main.
func zoneRepo(t *testing.T) (*testgit.Repo, *testgit.Remote, harness.Task) {
	t.Helper()
	src := testgit.New(t)
	src.Write("README.md", "bonjour\n")
	src.Write(optin.Path, optIn)
	src.Write(".github/workflows/ci.yml", "on: pull_request\n")
	src.Write("deploy/app.yaml", "replicas: 1\n")
	src.Commit("init")
	remote := testgit.NewRemote(t, "o/a", src)
	base := src.Out("rev-parse", "HEAD")
	return src, remote, harness.Task{
		Ticket:     forge.Ticket{Repo: "o/a", Number: 7, Title: "Tâche"},
		PassID:     "pass-42",
		BaseBranch: "main",
		BaseSHA:    base,
		OptInSHA:   base,
	}
}

// agentCommit commite tout l'état du dépôt de travail au nom de l'agent.
func agentCommit(src *testgit.Repo, msg string) {
	src.Git("add", "-A")
	src.Git("-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "-q", "-m", msg)
}

// series rend la série format-patch de base..HEAD du dépôt de travail.
func series(t *testing.T, src *testgit.Repo, base string) [][]byte {
	t.Helper()
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
	return patches
}

func publish(t *testing.T, remote *testgit.Remote, f *memforge.Forge, task harness.Task, patches [][]byte) (pass.Published, error) {
	t.Helper()
	p := publication.Git{Forge: f, GitURL: remote.Base}
	return p.Publish(context.Background(), task, harness.Result{Outcome: harness.Succeeded, Agent: "claude", Patches: patches})
}

// assertRefused vérifie un refus de zone qui nomme want, sans rien de
// poussé ni de PR ouverte.
func assertRefused(t *testing.T, remote *testgit.Remote, f *memforge.Forge, err error, want ...string) {
	t.Helper()
	var refusal *pass.ZoneRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("erreur = %v, attendu un refus de zone", err)
	}
	for _, w := range want {
		if !strings.Contains(refusal.Reason, w) {
			t.Fatalf("motif = %q, attendu %q", refusal.Reason, w)
		}
	}
	if branches := remote.Out("branch", "--list", "agent/*"); branches != "" {
		t.Fatalf("branche poussée malgré le refus : %q", branches)
	}
	if prs := f.DraftPRs("o/a"); len(prs) != 0 {
		t.Fatalf("PR ouverte malgré le refus : %+v", prs)
	}
}

func TestForbiddenZoneChangesAreRefusedBeforeThePush(t *testing.T) {
	for name, change := range map[string]func(src *testgit.Repo){
		"ajout":        func(src *testgit.Repo) { src.Write(".github/workflows/e9.yml", "on: push\n") },
		"modification": func(src *testgit.Repo) { src.Write(".github/workflows/ci.yml", "on: push\n") },
		"suppression":  func(src *testgit.Repo) { src.Git("rm", "-q", ".github/workflows/ci.yml") },
		"renommage vers la zone": func(src *testgit.Repo) {
			src.Git("mv", "README.md", ".github/README.md")
		},
		"renommage depuis la zone": func(src *testgit.Repo) {
			src.Git("mv", ".github/workflows/ci.yml", "ci.yml")
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, remote, task := zoneRepo(t)
			change(src)
			agentCommit(src, "chore: "+name)
			f := memforge.New()

			_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

			assertRefused(t, remote, f, err, "zone interdite", "`.github/**`")
		})
	}
}

func TestOptInRewrittenBySeriesIsRefused(t *testing.T) {
	src, remote, task := zoneRepo(t)
	src.Write(optin.Path, strings.Replace(optIn, `  - ".github/**"`, `  - "vendor/**"`, 1))
	agentCommit(src, "chore: assouplir l'adhésion")
	src.Write(".github/workflows/e9.yml", "on: push\n")
	agentCommit(src, "chore: workflow")
	f := memforge.New()

	_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

	assertRefused(t, remote, f, err, "`"+optin.ImplicitForbiddenZone+"`")
}

func TestCommitRevertedLaterInTheSeriesIsStillRefused(t *testing.T) {
	src, remote, task := zoneRepo(t)
	src.Write(".github/workflows/e9.yml", "on: push\n")
	agentCommit(src, "chore: workflow")
	src.Git("rm", "-q", ".github/workflows/e9.yml")
	src.Write("f.go", "package f\n")
	agentCommit(src, "feat: f, sans le workflow")
	if net := src.Out("diff", "--name-only", task.BaseSHA, "HEAD"); net != "f.go" {
		t.Fatalf("diff net = %q, attendu f.go seul", net)
	}
	f := memforge.New()

	_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

	assertRefused(t, remote, f, err, ".github/workflows/e9.yml")
}

func TestSymlinkInsideAForbiddenZoneIsRefused(t *testing.T) {
	src, remote, task := zoneRepo(t)
	src.Write("evil.yml", "on: push\n")
	if err := os.Symlink("../../evil.yml", filepath.Join(src.Dir, ".github/workflows/e9.yml")); err != nil {
		t.Fatal(err)
	}
	agentCommit(src, "chore: lien")
	f := memforge.New()

	_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

	assertRefused(t, remote, f, err, ".github/workflows/e9.yml")
}

func TestSymlinkOutsideZonesPointingIntoOneIsPushed(t *testing.T) {
	src, remote, task := zoneRepo(t)
	if err := os.Symlink(".github/workflows", filepath.Join(src.Dir, "workflows")); err != nil {
		t.Fatal(err)
	}
	agentCommit(src, "chore: lien vers les workflows")
	f := memforge.New()

	pub, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))
	if err != nil {
		t.Fatal(err)
	}

	if mode := remote.Out("ls-tree", pub.Head, "workflows"); !strings.HasPrefix(mode, "120000 ") {
		t.Fatalf("workflows poussé = %q, attendu un lien symbolique", mode)
	}
	if got := remote.Out("rev-parse", pub.Head+":.github/workflows/ci.yml"); got != remote.Out("rev-parse", task.BaseSHA+":.github/workflows/ci.yml") {
		t.Fatal("la zone interdite a changé")
	}
}

func TestPatchWritingThroughASymlinkIsNotApplied(t *testing.T) {
	src, remote, task := zoneRepo(t)
	// Sur la base, un lien hors zone pointe vers la zone interdite.
	if err := os.Symlink(".github", filepath.Join(src.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	src.Commit("lien")
	src.Git("push", "-q", remote.Dir(), "main")
	task.BaseSHA = src.Out("rev-parse", "HEAD")
	task.OptInSHA = task.BaseSHA
	through := []byte("From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001\nFrom: agent <agent@example.com>\nSubject: [PATCH] x\n\n---\n" +
		"diff --git a/link/workflows/e9.yml b/link/workflows/e9.yml\nnew file mode 100644\n--- /dev/null\n+++ b/link/workflows/e9.yml\n@@ -0,0 +1 @@\n+on: push\n")
	f := memforge.New()

	_, err := publish(t, remote, f, task, [][]byte{through})

	if err == nil || !strings.Contains(err.Error(), "ne s'applique pas") {
		t.Fatalf("erreur = %v, attendu le refus de git am d'écrire à travers un lien symbolique", err)
	}
	if branches := remote.Out("branch", "--list", "agent/*"); branches != "" {
		t.Fatalf("branche poussée : %q", branches)
	}
}

func TestMissingOrInvalidOptInAtBaseIsRefused(t *testing.T) {
	for name, edit := range map[string]func(src *testgit.Repo){
		"absente":  func(src *testgit.Repo) { src.Git("rm", "-q", optin.Path) },
		"invalide": func(src *testgit.Repo) { src.Write(optin.Path, "version: 99\n") },
	} {
		t.Run(name, func(t *testing.T) {
			src, remote, task := zoneRepo(t)
			edit(src)
			src.Commit("adhésion " + name)
			src.Git("push", "-q", remote.Dir(), "main")
			task.BaseSHA = src.Out("rev-parse", "HEAD")
			task.OptInSHA = task.BaseSHA
			src.Write("f.go", "package f\n")
			agentCommit(src, "feat: f")
			f := memforge.New()

			_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

			assertRefused(t, remote, f, err, "adhésion")
		})
	}
}

// firstRound publie un premier tour sans zone touchée et rend la tâche du
// tour de relance, sur la tête de la PR.
func firstRound(t *testing.T, src *testgit.Repo, remote *testgit.Remote, f *memforge.Forge, task harness.Task) harness.Task {
	t.Helper()
	src.Write("f.go", "package f\n")
	agentCommit(src, "feat: f")
	first, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))
	if err != nil {
		t.Fatal(err)
	}
	// Comme prepareRetry, l'agent relancé repart de la tête publiée.
	src.Git("fetch", "-q", remote.Dir(), first.Draft.Branch)
	src.Git("reset", "-q", "--hard", first.Head)
	retry := task
	retry.Round, retry.BaseSHA, retry.PullRequest = 2, first.Head, &first.Draft
	return retry
}

func TestRetryRoundTouchingAForbiddenZoneLeavesThePullRequestAlone(t *testing.T) {
	src, remote, task := zoneRepo(t)
	f := memforge.New()
	retry := firstRound(t, src, remote, f, task)
	src.Write(".github/workflows/e9.yml", "on: push\n")
	agentCommit(src, "fix: workflow")

	_, err := publish(t, remote, f, retry, series(t, src, retry.BaseSHA))

	var refusal *pass.ZoneRefusalError
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, ".github/workflows/e9.yml") {
		t.Fatalf("erreur = %v, attendu un refus de zone", err)
	}
	if got := remote.Out("rev-parse", retry.PullRequest.Branch); got != retry.BaseSHA {
		t.Fatalf("tête de la PR = %s, attendu inchangée %s", got, retry.BaseSHA)
	}
	if len(f.DraftPRs("o/a")) != 1 {
		t.Fatal("une autre PR a été ouverte")
	}
}

func TestRetryRoundReadsTheOptInOfTheFirstRoundBase(t *testing.T) {
	src, remote, task := zoneRepo(t)
	f := memforge.New()
	retry := firstRound(t, src, remote, f, task)
	// Un humain retire .github/** de l'adhésion sur la branche de la PR :
	// la relance reste jugée par l'adhésion du commit de base.
	src.Write(optin.Path, strings.Replace(optIn, `  - ".github/**"`, `  - "vendor/**"`, 1))
	src.Commit("humain : adhésion")
	src.Git("push", "-q", remote.Dir(), "HEAD:"+retry.PullRequest.Branch)
	retry.BaseSHA = src.Out("rev-parse", "HEAD")
	src.Write(".github/workflows/e9.yml", "on: push\n")
	agentCommit(src, "fix: workflow")

	_, err := publish(t, remote, f, retry, series(t, src, retry.BaseSHA))

	var refusal *pass.ZoneRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("erreur = %v, attendu un refus de zone", err)
	}
	if got := remote.Out("rev-parse", retry.PullRequest.Branch); got != retry.BaseSHA {
		t.Fatal("la branche de la PR a bougé")
	}
}

func TestRetryRoundWithoutOptInCommitIsRefused(t *testing.T) {
	src, remote, task := zoneRepo(t)
	f := memforge.New()
	retry := firstRound(t, src, remote, f, task)
	retry.OptInSHA = ""
	src.Write("g.go", "package f\n")
	agentCommit(src, "fix: g")

	_, err := publish(t, remote, f, retry, series(t, src, retry.BaseSHA))

	var refusal *pass.ZoneRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("erreur = %v, attendu un refus de zone", err)
	}
}

func TestSensitiveZoneIsPushedAndFlaggedOnTheDraft(t *testing.T) {
	src, remote, task := zoneRepo(t)
	src.Write("deploy/app.yaml", "replicas: 3\n")
	agentCommit(src, "chore: replicas")
	f := memforge.New()

	pub, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))
	if err != nil {
		t.Fatal(err)
	}

	if got := remote.Out("rev-parse", pub.Draft.Branch); got != pub.Head {
		t.Fatal("série non poussée")
	}
	pr := f.Pull("o/a", pub.Draft.Number)
	if !slices.Contains(pr.Labels, forge.LabelSensitiveZone) {
		t.Fatalf("labels de la PR = %v, attendu %s", pr.Labels, forge.LabelSensitiveZone)
	}
	if !strings.Contains(pr.Body, "`deploy/app.yaml`") || !strings.Contains(pr.Body, "`deploy/**`") {
		t.Fatalf("corps de la PR sans mention de la zone sensible :\n%s", pr.Body)
	}
}

func TestSensitiveZoneOnRetryRoundLabelsAndCommentsThePullRequest(t *testing.T) {
	src, remote, task := zoneRepo(t)
	f := memforge.New()
	retry := firstRound(t, src, remote, f, task)
	if pr := f.Pull("o/a", retry.PullRequest.Number); len(pr.Labels) != 0 || len(pr.Comments) != 0 {
		t.Fatalf("PR du premier tour = %+v, attendu sans label ni commentaire", pr)
	}
	src.Write("deploy/app.yaml", "replicas: 3\n")
	agentCommit(src, "fix: replicas")

	if _, err := publish(t, remote, f, retry, series(t, src, retry.BaseSHA)); err != nil {
		t.Fatal(err)
	}

	pr := f.Pull("o/a", retry.PullRequest.Number)
	if !slices.Contains(pr.Labels, forge.LabelSensitiveZone) || len(pr.Comments) != 1 || !strings.Contains(pr.Comments[0], "`deploy/app.yaml`") {
		t.Fatalf("PR = %+v, attendu le label et un commentaire qui nomme le chemin", pr)
	}
}

func TestPathsInFindingsCannotEscapeTheirCodeSpan(t *testing.T) {
	src, remote, task := zoneRepo(t)
	src.Write(".github/a`@someone`\nb.yml", "x\n")
	agentCommit(src, "chore: nom piégé")
	f := memforge.New()

	_, err := publish(t, remote, f, task, series(t, src, task.BaseSHA))

	var refusal *pass.ZoneRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("erreur = %v, attendu un refus de zone", err)
	}
	if strings.Contains(refusal.Reason, "\n") || strings.Count(refusal.Reason, "`") != 4 {
		t.Fatalf("motif = %q, attendu chaque chemin dans un seul span de code", refusal.Reason)
	}
}
