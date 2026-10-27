package relay_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/relay/relaytest"
)

func TestTurnAndDoneRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := relay.ReadTurn(dir); ok || err != nil {
		t.Fatalf("aucun marqueur attendu : ok=%v err=%v", ok, err)
	}
	deadline := time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)
	turn := relay.Turn{Pass: "pass-1", Round: 2, Ticket: true, Deadline: deadline}
	if err := relay.WriteTurn(dir, turn); err != nil {
		t.Fatal(err)
	}
	got, ok, err := relay.ReadTurn(dir)
	if err != nil || !ok || got != turn {
		t.Fatalf("marqueur relu = %+v, %v, %v", got, ok, err)
	}
	if got.Expired(deadline.Add(-time.Second)) || !got.Expired(deadline) {
		t.Fatal("échéance mal jugée")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("fichiers = %v, attendu le seul marqueur, sans fichier temporaire", entries)
	}

	if err := relay.WriteDone(dir, relay.Done{Pass: "pass-1", Round: 2}); err != nil {
		t.Fatal(err)
	}
	d, ok, err := relay.ReadDone(dir)
	if err != nil || !ok || !d.Matches(turn) || (relay.Done{Pass: "pass-0", Round: 2}).Matches(turn) {
		t.Fatalf("fin relue = %+v, %v, %v", d, ok, err)
	}
}

func TestDoneWrittenByTheAgentIsReadAsUntrusted(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte(`{"pass":"ghp_secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(out string) error{
		"lien": func(out string) error { return os.Symlink(secret, filepath.Join(out, relay.DoneFile)) },
		"trop gros": func(out string) error {
			return os.WriteFile(filepath.Join(out, relay.DoneFile), make([]byte, 1<<20), 0o600)
		},
		"illisible": func(out string) error { return os.WriteFile(filepath.Join(out, relay.DoneFile), []byte("{"), 0o600) },
		"dossier":   func(out string) error { return os.Mkdir(filepath.Join(out, relay.DoneFile), 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			if err := plant(out); err != nil {
				t.Fatal(err)
			}
			d, _, err := relay.ReadDone(out)
			if err == nil {
				t.Fatalf("marqueur accepté : %+v", d)
			}
		})
	}
}

func TestClaimWaitsForARequestedPass(t *testing.T) {
	srv := relaytest.New(t)
	c := relay.Client{BaseURL: srv.URL}
	ctx := context.Background()

	if _, ok, err := c.Claim(ctx, 10*time.Millisecond); ok || err != nil {
		t.Fatalf("aucune passe attendue : ok=%v err=%v", ok, err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		srv.Request("pass-1", relay.Manual)
	}()
	cl, ok, err := c.Claim(ctx, 5*time.Second)
	if err != nil || !ok || cl.Pass != "pass-1" || cl.Trigger != relay.Manual {
		t.Fatalf("passe réclamée = %+v, %v, %v", cl, ok, err)
	}
}

func TestEventsReachTheServiceUntilThePassIsClosed(t *testing.T) {
	srv := relaytest.New(t)
	c := relay.Client{BaseURL: srv.URL}
	ctx := context.Background()
	srv.Request("pass-1", relay.Schedule)

	if err := c.Send(ctx, "pass-inconnue", relay.Event{Stage: "select", Type: relay.Started}); !errors.Is(err, relay.ErrUnknownPass) {
		t.Fatalf("erreur = %v, attendu ErrUnknownPass", err)
	}
	exit := 0
	for _, e := range []relay.Event{
		{Stage: "select", Type: relay.Started, At: time.Now()},
		{Stage: "select", Type: relay.Incident, Detail: "préparation du clone : refus"},
		{Stage: "select", Type: relay.Finished, Exit: &exit},
		{Stage: "publish", Type: relay.PassFinished, Report: &pass.Report{ID: "pass-1", Errors: []string{"x"}}},
	} {
		if err := c.Send(ctx, "pass-1", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Send(ctx, "pass-1", relay.Event{Stage: "retry-publish", Type: relay.Started}); !errors.Is(err, relay.ErrPassClosed) {
		t.Fatalf("erreur = %v, attendu ErrPassClosed après pass-finished", err)
	}
	got := srv.Events("pass-1")
	if len(got) != 4 || got[1].Detail == "" || *got[2].Exit != 0 || got[3].Report == nil || got[3].Report.ID != "pass-1" {
		t.Fatalf("événements reçus = %+v", got)
	}
}
