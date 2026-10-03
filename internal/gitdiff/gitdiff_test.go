package gitdiff_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/gitdiff"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
	"github.com/UnPoilTefal/night-shift/internal/zones"
)

func TestChangesAndContents(t *testing.T) {
	repo := testgit.New(t)
	repo.Write("README.md", "base\n")
	repo.Write(".github/CODEOWNERS", "* @alice\n")
	repo.Commit("base")

	repo.Git("checkout", "-q", "-b", "agent/1-demo")
	repo.Write("README.md", "head\n")
	repo.Git("mv", ".github/CODEOWNERS", "docs-CODEOWNERS")
	repo.Write("new.txt", "new\n")
	repo.Commit("head")

	// La base avance après la divergence : ses changements ne doivent pas
	// apparaître dans le diff de la PR.
	repo.Git("checkout", "-q", "main")
	repo.Write("unrelated.txt", "x\n")
	repo.Commit("base advances")

	r, err := gitdiff.Open(repo.Dir, "main", "agent/1-demo")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := r.Changes()
	if err != nil {
		t.Fatal(err)
	}
	want := []zones.Change{
		{Path: "README.md"},
		{Path: "docs-CODEOWNERS", OldPath: ".github/CODEOWNERS"},
		{Path: "new.txt"},
	}
	slices.SortFunc(cs, func(a, b zones.Change) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(cs, want) {
		t.Fatalf("changements = %+v, attendu %+v", cs, want)
	}

	if data, ok, err := r.Base("README.md"); err != nil || !ok || string(data) != "base\n" {
		t.Fatalf("Base(README.md) = %q, %v, %v", data, ok, err)
	}
	if data, ok, err := r.Head("README.md"); err != nil || !ok || string(data) != "head\n" {
		t.Fatalf("Head(README.md) = %q, %v, %v", data, ok, err)
	}
	if _, ok, err := r.Base("new.txt"); err != nil || ok {
		t.Fatalf("Base(new.txt) : ok = %v, err = %v, attendu absent", ok, err)
	}
}

func TestOpenRejectsOptionLikeRevisions(t *testing.T) {
	repo := testgit.New(t)
	repo.Write("README.md", "x\n")
	repo.Commit("base")
	for _, rev := range []string{"", "--output=/tmp/pwned", "-p"} {
		if _, err := gitdiff.Open(repo.Dir, rev, "main"); err == nil {
			t.Fatalf("révision %q acceptée", rev)
		}
	}
}
