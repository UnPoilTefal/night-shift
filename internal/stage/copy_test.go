package stage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/harness"
)

func TestCopyOutIsWhatTheResultIsReadFromAfterward(t *testing.T) {
	d := dirs(t)
	if err := d.WriteResult(harness.Result{Outcome: harness.Succeeded, Patches: [][]byte{[]byte("p1")}, SessionID: "s", Transcript: harness.TranscriptFile}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.OutDir(), harness.TranscriptFile), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := d.CopyOut(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// L'agent réécrit sa sortie après la copie.
	if err := d.WriteResult(harness.Result{Outcome: harness.Failed, Reason: "réécrit"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.ReadResult()
	if err != nil || !ok || got.Outcome != harness.Succeeded || len(got.Patches) != 1 || string(got.Patches[0]) != "p1" {
		t.Fatalf("résultat de la copie = %+v, %v, %v", got, ok, err)
	}
	if b, err := os.ReadFile(filepath.Join(c.OutDir(), harness.TranscriptFile)); err != nil || string(b) != "{}\n" {
		t.Fatalf("transcript copié = %q, %v", b, err)
	}
}

func TestCopyOutRefusesLinksAndAcceptsAnAbsentOutput(t *testing.T) {
	d := dirs(t)
	if _, err := d.CopyOut(t.TempDir()); err != nil {
		t.Fatalf("sortie absente : %v", err)
	}
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteResult(harness.Result{Outcome: harness.Succeeded, Patches: [][]byte{[]byte("p")}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d.OutDir(), "patches", "0001.patch")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, p); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CopyOut(t.TempDir()); err == nil || strings.Contains(err.Error(), "ghp_secret") {
		t.Fatalf("erreur = %v, attendu un refus du lien sans son contenu", err)
	}
}
