package stage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

func dirs(t *testing.T) stage.Dirs {
	return stage.Dirs{State: t.TempDir(), Work: t.TempDir()}
}

func TestTaskRoundTrip(t *testing.T) {
	d := dirs(t)
	if _, ok, err := d.ReadTask(); ok || err != nil {
		t.Fatalf("aucune tâche attendue avant la sélection : ok=%v err=%v", ok, err)
	}
	want := harness.Task{
		Ticket: forge.Ticket{Repo: "o/a", Number: 7, Title: "T", Body: "B", Author: "alice"},
		Brief:  "# T\n\nB\n", PassID: "pass-1", BaseBranch: "main", BaseSHA: "abc",
	}
	if err := d.WriteTask(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ReadTask()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Ticket.Repo != "o/a" || got.Ticket.Number != 7 || got.Brief != want.Brief || got.BaseSHA != "abc" || got.PassID != "pass-1" {
		t.Fatalf("tâche relue = %+v", got)
	}
}

func TestTaskWrittenWithoutTargetRepoTargetsTheTicketRepo(t *testing.T) {
	d := dirs(t)
	// Une tâche de relance écrite par la version précédente : ni dépôt cible,
	// ni identité de l'agent.
	legacy := `{
  "Ticket": {"Repo": "o/a", "Number": 7, "Title": "T"},
  "Brief": "# T\n", "PassID": "pass-1", "BaseBranch": "main", "BaseSHA": "def", "OptInSHA": "abc",
  "Round": 2, "PullRequest": {"Number": 12, "URL": "https://forge/o/a/pull/12", "Branch": "agent/7-t"}
}`
	if err := os.WriteFile(filepath.Join(d.State, "task.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ReadTask()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.TargetRepo != "o/a" {
		t.Fatalf("dépôt cible = %q, attendu celui du ticket, o/a", got.TargetRepo)
	}
	if cr := got.ChangeRequest; cr == nil || cr.Number != 12 || cr.Branch != "agent/7-t" {
		t.Fatalf("demande de changement relue = %+v", cr)
	}
}

func TestReportRoundTrip(t *testing.T) {
	d := dirs(t)
	if _, ok, err := d.ReadReport(); ok || err != nil {
		t.Fatalf("aucun rapport attendu avant la sélection : ok=%v err=%v", ok, err)
	}
	want := pass.Report{
		ID:        "pass-1",
		Repos:     []pass.RepoReport{{Repo: "o/a", Status: pass.Eligible}, {Repo: "o/b", Status: pass.InvalidOptIn, Detail: "version 7"}},
		Tickets:   []pass.TicketReport{{Repo: "o/a", Number: 3, Outcome: harness.Failed, Reason: "clone", CostUSD: 0.1, Duration: time.Second}},
		Retriaged: []pass.RetriageReport{{Repo: "o/a", Number: 4, Reason: "tiers"}},
	}
	if err := d.WriteReport(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ReadReport()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.ID != "pass-1" || len(got.Repos) != 2 || got.Repos[1].Detail != "version 7" ||
		len(got.Tickets) != 1 || got.Tickets[0].Duration != time.Second || len(got.Retriaged) != 1 {
		t.Fatalf("rapport relu = %+v", got)
	}
}

func TestResultRoundTripKeepsPatchOrder(t *testing.T) {
	d := dirs(t)
	if _, ok, err := d.ReadResult(); ok || err != nil {
		t.Fatalf("aucun résultat attendu avant l'agent : ok=%v err=%v", ok, err)
	}
	var patches [][]byte
	for i := range 12 {
		patches = append(patches, []byte(strings.Repeat("x", i+1)))
	}
	want := harness.Result{Outcome: harness.Succeeded, Reason: "ok", Agent: "claude", Patches: patches,
		CostUSD: 0.5, Duration: time.Minute, SessionID: "s", Transcript: harness.TranscriptFile}
	if err := d.WriteResult(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ReadResult()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(got.Patches) != 12 || string(got.Patches[11]) != strings.Repeat("x", 12) || got.Duration != time.Minute || got.Transcript != harness.TranscriptFile {
		t.Fatalf("résultat relu = %+v", got)
	}
}

func TestResultIgnoresATranscriptPathChosenByTheAgent(t *testing.T) {
	for _, path := range []string{"/transcripts/autre.jsonl", "../../state/task.json", "out/transcript.jsonl"} {
		d := dirs(t)
		if err := d.WriteResult(harness.Result{Outcome: harness.Stopped, SessionID: "s", Transcript: path}); err != nil {
			t.Fatal(err)
		}
		got, _, err := d.ReadResult()
		if err != nil || got.Transcript != "" {
			t.Fatalf("%q : transcript relu = %q, %v ; attendu aucun", path, got.Transcript, err)
		}
	}
}

// withTranscript écrit un résultat qui renvoie au transcript, de contenu
// donné.
func withTranscript(t *testing.T, content []byte) stage.Dirs {
	t.Helper()
	d := dirs(t)
	if err := d.WriteResult(harness.Result{Outcome: harness.Stopped, SessionID: "s", Transcript: harness.TranscriptFile}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.OutDir(), harness.TranscriptFile), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func readResult(t *testing.T, d stage.Dirs) harness.Result {
	t.Helper()
	res, _, err := d.ReadResult()
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTranscriptIsArchivedAsIsUnderANameFromTheTask(t *testing.T) {
	d := withTranscript(t, []byte("{\"type\":\"user\"}\n"))
	archive := t.TempDir()
	task := harness.Task{Ticket: forge.Ticket{Repo: "group/sub/../a", Number: 7}, PassID: "pass-20261027T020000Z", Round: 2}

	path, err := d.ArchiveTranscript(readResult(t, d), task, archive)

	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(archive, "pass-20261027T020000Z", "group_sub_.._a-7-round2.jsonl"); path != want {
		t.Fatalf("archive = %q, attendu %q", path, want)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "{\"type\":\"user\"}\n" {
		t.Fatalf("archive = %q, %v ; attendu la copie telle quelle", b, err)
	}
	if _, err := d.ArchiveTranscript(readResult(t, d), task, archive); err == nil {
		t.Fatal("une archive existante a été écrasée")
	}
}

func TestTranscriptArchiveRefusesWhatIsNotAPlainBoundedFile(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := harness.Task{Ticket: forge.Ticket{Repo: "o/a", Number: 7}, PassID: "pass-1"}
	for name, d := range map[string]stage.Dirs{
		"trop gros": withTranscript(t, make([]byte, harness.MaxTranscriptBytes+1)),
		"lien": func() stage.Dirs {
			d := withTranscript(t, nil)
			p := filepath.Join(d.OutDir(), harness.TranscriptFile)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, p); err != nil {
				t.Fatal(err)
			}
			return d
		}(),
		"absent": func() stage.Dirs {
			d := withTranscript(t, nil)
			if err := os.Remove(filepath.Join(d.OutDir(), harness.TranscriptFile)); err != nil {
				t.Fatal(err)
			}
			return d
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			archive := t.TempDir()
			path, err := d.ArchiveTranscript(readResult(t, d), task, archive)
			if err == nil || path != "" {
				t.Fatalf("archive = %q, %v ; attendu un refus", path, err)
			}
			if strings.Contains(err.Error(), "ghp_secret") {
				t.Fatal("le contenu de la cible fuit dans l'erreur")
			}
			if entries, _ := os.ReadDir(filepath.Join(archive, "pass-1")); len(entries) != 0 {
				t.Fatalf("archive laissée après un refus : %v", entries)
			}
		})
	}
}

func TestMissingTranscriptIsAnIncidentOnlyWhenASessionRan(t *testing.T) {
	task := harness.Task{Ticket: forge.Ticket{Repo: "o/a", Number: 7}, PassID: "pass-1"}
	d := dirs(t)
	if path, err := d.ArchiveTranscript(harness.Result{Outcome: harness.Failed}, task, t.TempDir()); err != nil || path != "" {
		t.Fatalf("agent non démarré : %q, %v ; attendu rien à archiver", path, err)
	}
	if _, err := d.ArchiveTranscript(harness.Result{Outcome: harness.Stopped, SessionID: "s"}, task, t.TempDir()); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("session sans transcript : %v, attendu un incident", err)
	}
}

func TestResultRefusesSymlinks(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(d stage.Dirs) error{
		"patch": func(d stage.Dirs) error {
			return os.Symlink(secret, filepath.Join(d.OutDir(), "patches", "0002.patch"))
		},
		"dossier patches": func(d stage.Dirs) error {
			elsewhere := t.TempDir()
			if err := os.WriteFile(filepath.Join(elsewhere, "0001.patch"), []byte("ghp_secret"), 0o600); err != nil {
				return err
			}
			p := filepath.Join(d.OutDir(), "patches")
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return os.Symlink(elsewhere, p)
		},
		"result.json": func(d stage.Dirs) error {
			p := filepath.Join(d.OutDir(), "result.json")
			if err := os.Remove(p); err != nil {
				return err
			}
			return os.Symlink(secret, p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := dirs(t)
			if err := d.WriteResult(harness.Result{Outcome: harness.Succeeded, Patches: [][]byte{[]byte("p")}}); err != nil {
				t.Fatal(err)
			}
			if err := plant(d); err != nil {
				t.Fatal(err)
			}
			res, _, err := d.ReadResult()
			if err == nil {
				t.Fatalf("lien symbolique accepté : %+v", res)
			}
			if strings.Contains(err.Error(), "ghp_secret") {
				t.Fatal("le contenu de la cible fuit dans l'erreur")
			}
		})
	}
}

func TestResultRejectsUnknownOutcomeAndBoundsReason(t *testing.T) {
	d := dirs(t)
	if err := d.WriteResult(harness.Result{Outcome: "merged", Reason: strings.Repeat("é", 10000)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ReadResult()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Outcome != harness.Failed {
		t.Fatalf("issue = %q, attendu failed pour une issue inconnue", got.Outcome)
	}
	if n := len([]rune(got.Reason)); n > stage.MaxReason+1 {
		t.Fatalf("motif de %d caractères, attendu au plus %d", n, stage.MaxReason)
	}
}
