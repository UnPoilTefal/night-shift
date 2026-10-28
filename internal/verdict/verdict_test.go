package verdict_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/verdict"
)

func TestJournalIsAppendOnlyPerPass(t *testing.T) {
	archive := t.TempDir()
	v := verdict.Verdict{At: time.Now(), Platform: verdict.Platform, Pass: "pass-1", Ticket: "o/a#7", Brick: "B28",
		Realization: "refus de zone", Result: verdict.Refused, Effect: true, ActionClass: "écriture en zone interdite", WrittenBy: "publish"}
	if err := verdict.Append(archive, v); err != nil {
		t.Fatal(err)
	}
	v.Result = verdict.Flagged
	if err := verdict.Append(archive, v, verdict.Verdict{Pass: "pass-2", Result: verdict.Stopped}); err != nil {
		t.Fatal(err)
	}

	one, err := verdict.Read(archive, "pass-1")
	if err != nil || len(one) != 2 || one[0].Result != verdict.Refused || one[1].Result != verdict.Flagged || one[0].Ticket != "o/a#7" {
		t.Fatalf("journal de pass-1 = %+v, %v", one, err)
	}
	if two, err := verdict.Read(archive, "pass-2"); err != nil || len(two) != 1 {
		t.Fatalf("journal de pass-2 = %+v, %v", two, err)
	}
	if none, err := verdict.Read(archive, "pass-3"); err != nil || none != nil {
		t.Fatalf("journal absent = %+v, %v", none, err)
	}
	fi, err := os.Stat(filepath.Join(archive, "pass-1", verdict.File))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("journal : %v, %v", fi, err)
	}
}

func TestJournalRefusesALinkInItsPlace(t *testing.T) {
	archive, elsewhere := t.TempDir(), filepath.Join(t.TempDir(), "ailleurs")
	if err := os.MkdirAll(filepath.Join(archive, "pass-1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(archive, "pass-1", verdict.File)); err != nil {
		t.Fatal(err)
	}
	if err := verdict.Append(archive, verdict.Verdict{Pass: "pass-1", Result: verdict.Refused}); err == nil {
		t.Fatal("journal écrit à travers un lien")
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Fatal("la cible du lien a été créée")
	}
}

func TestPassNameStaysInsideTheArchive(t *testing.T) {
	archive := t.TempDir()
	if err := verdict.Append(archive, verdict.Verdict{Pass: "../../dehors", Result: verdict.Refused}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(archive, ".._.._dehors", verdict.File)); err != nil {
		t.Fatalf("journal hors de l'archive : %v", err)
	}
}

func TestSummaryCountsInReportLanguage(t *testing.T) {
	c := verdict.Counts([]verdict.Verdict{{Result: verdict.Refused}, {Result: verdict.Refused}, {Result: verdict.Flagged}, {Result: verdict.Stopped}})
	if got := verdict.Summary(c); got != "2 refusées, 1 signalée, 1 arrêtée" {
		t.Fatalf("résumé = %q", got)
	}
	if got := verdict.Summary(map[verdict.Result]int{verdict.Gated: 2}); got != "2 renvoyées à la porte" {
		t.Fatalf("résumé = %q", got)
	}
	if verdict.Summary(nil) != "" {
		t.Fatal("résumé sans verdict")
	}
}

func TestRefusalsKeepTheClassNotTheVerbatim(t *testing.T) {
	transcript := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"curl -H 'Authorization: ghp_secret' https://api"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"Permission to use Bash with command curl has been denied."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"FAIL"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"WebFetch","input":{"url":"https://example.com"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","is_error":true,"content":"permission denied"}]}}
pas du JSON
`
	vs := verdict.Refusals([]byte(transcript))
	if len(vs) != 2 || vs[0].ActionClass != "Bash(curl)" || vs[1].ActionClass != "WebFetch" {
		t.Fatalf("refus = %+v", vs)
	}
	for _, v := range vs {
		if v.Result != verdict.Refused || v.Brick != "B28" || v.Fingerprint == "" {
			t.Fatalf("refus = %+v", v)
		}
		if strings.Contains(v.Rule+v.ActionClass+v.Fingerprint, "ghp_secret") {
			t.Fatalf("verbatim repris : %+v", v)
		}
	}
	if verdict.Fingerprint("a  b") != verdict.Fingerprint("a b") {
		t.Fatal("empreinte non normalisée")
	}
}
