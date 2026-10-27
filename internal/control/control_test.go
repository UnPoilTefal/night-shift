package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/UnPoilTefal/night-shift/internal/control"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/relay"
)

// clock est une horloge simulée.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var start = time.Date(2026, 10, 27, 1, 59, 30, 0, time.UTC)

// service démarre un service sur une horloge simulée et rend son client.
func service(t *testing.T, edit func(*control.Config)) (*control.Service, relay.Client, *clock, control.Config) {
	t.Helper()
	clk := &clock{t: start}
	cfg := control.Config{History: t.TempDir(), Now: clk.Now, Watchdog: time.Hour}
	if edit != nil {
		edit(&cfg)
	}
	svc, err := control.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.StagesHandler())
	t.Cleanup(srv.Close)
	return svc, relay.Client{BaseURL: srv.URL}, clk, cfg
}

func claim(t *testing.T, c relay.Client) (relay.Claim, bool) {
	t.Helper()
	cl, ok, err := c.Claim(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return cl, ok
}

func finish(t *testing.T, c relay.Client, id string) {
	t.Helper()
	if err := c.Send(context.Background(), id, relay.Event{Stage: "publish", Type: relay.PassFinished, Report: &pass.Report{ID: id}}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestsDuringAPassMergeIntoExactlyOnePassToFollow(t *testing.T) {
	svc, c, clk, _ := service(t, nil)

	svc.Request(relay.Manual)
	first, ok := claim(t, c)
	if !ok || first.Pass != "pass-20261027T015930Z" || first.Trigger != relay.Manual {
		t.Fatalf("première passe = %+v, %v", first, ok)
	}
	for range 3 {
		svc.Request(relay.Manual)
	}
	if cl, ok := claim(t, c); ok {
		t.Fatalf("passe %s rendue pendant que %s est active", cl.Pass, first.Pass)
	}
	finish(t, c, first.Pass)

	second, ok := claim(t, c)
	if !ok || second.Pass == first.Pass {
		t.Fatalf("passe à suivre = %+v, %v", second, ok)
	}
	clk.Set(start.Add(time.Minute))
	finish(t, c, second.Pass)
	if cl, ok := claim(t, c); ok {
		t.Fatalf("passe %s en trop : les trois demandes devaient n'en faire qu'une", cl.Pass)
	}
}

func TestClaimWaitsForARequest(t *testing.T) {
	svc, c, _, _ := service(t, nil)
	go func() {
		time.Sleep(30 * time.Millisecond)
		svc.Request(relay.Manual)
	}()
	cl, ok, err := c.Claim(context.Background(), 5*time.Second)
	if err != nil || !ok || cl.Pass == "" {
		t.Fatalf("réclamation = %+v, %v, %v", cl, ok, err)
	}
}

func TestPassIDsAreUniqueWithinTheSameSecond(t *testing.T) {
	svc, c, _, _ := service(t, nil)
	seen := map[string]bool{}
	for range 3 {
		svc.Request(relay.Manual)
		cl, ok := claim(t, c)
		if !ok || seen[cl.Pass] {
			t.Fatalf("identifiant %q rendu deux fois (ok=%v)", cl.Pass, ok)
		}
		seen[cl.Pass] = true
		finish(t, c, cl.Pass)
	}
}

func TestWatchdogFailsAStuckPassAndTheNextOneStarts(t *testing.T) {
	svc, c, clk, cfg := service(t, nil)
	svc.Request(relay.Schedule)
	stuck, _ := claim(t, c)
	svc.Request(relay.Manual)

	if err := svc.Tick(start.Add(59 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if svc.Status().Active == nil {
		t.Fatal("passe close avant son échéance")
	}
	clk.Set(start.Add(time.Hour))
	if err := svc.Tick(clk.Now()); err != nil {
		t.Fatal(err)
	}

	next, ok := claim(t, c)
	if !ok || next.Pass == stuck.Pass {
		t.Fatalf("passe suivante = %+v, %v ; attendu le démarrage de la passe en attente", next, ok)
	}
	if err := c.Send(context.Background(), stuck.Pass, relay.Event{Stage: "publish", Type: relay.Finished}); !errors.Is(err, relay.ErrPassClosed) {
		t.Fatalf("événement tardif : %v, attendu ErrPassClosed", err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.History, stuck.Pass+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec control.Record
	if err := json.Unmarshal(b, &rec); err != nil || rec.Status != control.Failed || !strings.Contains(rec.Reason, "durée maximale") {
		t.Fatalf("historique = %+v, %v ; attendu la passe échouée et motivée", rec, err)
	}
}

func TestEventsBuildTheHistoryAndSurviveARestart(t *testing.T) {
	svc, c, _, cfg := service(t, nil)
	ctx := context.Background()
	if err := c.Send(ctx, "pass-20261027T000000Z", relay.Event{Stage: "select", Type: relay.Started}); !errors.Is(err, relay.ErrUnknownPass) {
		t.Fatalf("passe inconnue : %v", err)
	}
	svc.Request(relay.Manual)
	cl, _ := claim(t, c)
	exit := 1
	report := &pass.Report{ID: cl.Pass, Tickets: []pass.TicketReport{{Repo: "o/a", Number: 7}},
		Repos: []pass.RepoReport{{Repo: "o/b", Status: pass.NoOptIn}}, Errors: []string{"préparation du clone"}}
	for _, e := range []relay.Event{
		{Stage: "select", Type: relay.Started},
		{Stage: "select", Type: relay.Incident, Detail: "préparation du clone : dépôt injoignable (détail)"},
		{Stage: "select", Type: relay.Finished, Exit: &exit},
		{Stage: "publish", Type: relay.PassFinished, Report: report},
	} {
		if err := c.Send(ctx, cl.Pass, e); err != nil {
			t.Fatal(err)
		}
	}

	again, err := control.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(again.StagesHandler())
	defer srv.Close()
	if err := (relay.Client{BaseURL: srv.URL}).Send(ctx, cl.Pass, relay.Event{Stage: "publish", Type: relay.Started}); !errors.Is(err, relay.ErrPassClosed) {
		t.Fatalf("après redémarrage : %v, attendu ErrPassClosed", err)
	}
	admin := httptest.NewServer(again.AdminHandler("admin-token"))
	defer admin.Close()
	var rec control.Record
	get(t, admin.URL+control.AdminPrefix+"/passes/"+cl.Pass, "admin-token", http.StatusOK, &rec)
	if rec.Status != control.Finished || len(rec.Events) != 4 || rec.Events[1].Detail == "" || rec.Events[3].Report.Repos[0].Status != pass.NoOptIn {
		t.Fatalf("historique = %+v", rec)
	}
	var list []control.Summary
	get(t, admin.URL+control.AdminPrefix+"/passes", "admin-token", http.StatusOK, &list)
	if len(list) != 1 || list[0].Tickets != 1 || list[0].Incidents != 1 || list[0].Status != control.Finished {
		t.Fatalf("liste = %+v", list)
	}
}

func get(t *testing.T, url, token string, code int, v any) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != code {
		t.Fatalf("GET %s : %d, attendu %d\n%s", url, resp.StatusCode, code, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	return string(b)
}

func TestAdminAPIRefusesWithoutTheRightTokenAndNeverQuotesIt(t *testing.T) {
	svc, _, _, _ := service(t, nil)
	const token = "le-bon-jeton-0123456789"
	admin := httptest.NewServer(svc.AdminHandler(token))
	defer admin.Close()

	for _, wrong := range []string{"", "le-bon-jeton", "faux-jeton-0123456789"} {
		body := get(t, admin.URL+control.AdminPrefix+"/status", wrong, http.StatusUnauthorized, nil)
		if strings.Contains(body, token) || (wrong != "" && strings.Contains(body, wrong)) {
			t.Fatalf("le refus cite un jeton : %q", body)
		}
	}
	var st control.Status
	get(t, admin.URL+control.AdminPrefix+"/status", token, http.StatusOK, &st)
	if st.Active != nil {
		t.Fatalf("état = %+v", st)
	}
	// Lecture seule : aucune méthode d'écriture n'est servie.
	req, _ := http.NewRequest(http.MethodPost, admin.URL+control.AdminPrefix+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST sur l'administration : %d", resp.StatusCode)
	}
	// Sans jeton configuré, l'administration refuse tout.
	closed := httptest.NewServer(svc.AdminHandler(""))
	defer closed.Close()
	get(t, closed.URL+control.AdminPrefix+"/status", "", http.StatusUnauthorized, nil)
}

func TestPurgeRemovesOnlyWhatIsOlderThanTheRetention(t *testing.T) {
	history, transcripts := t.TempDir(), t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	files := map[string]time.Duration{
		filepath.Join(history, "pass-20260901T020000Z.json"):                      31 * 24 * time.Hour,
		filepath.Join(history, "pass-20261001T020000Z.json"):                      29 * 24 * time.Hour,
		filepath.Join(transcripts, "pass-20260901T020000Z", "o_a-7-round1.jsonl"): 31 * 24 * time.Hour,
		filepath.Join(transcripts, "pass-20261001T020000Z", "o_a-8-round1.jsonl"): 29 * 24 * time.Hour,
	}
	for path, age := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	svc, _, _, _ := service(t, func(c *control.Config) {
		c.History, c.Transcripts = history, transcripts
		c.Now = func() time.Time { return now }
	})
	if err := svc.Tick(now); err != nil {
		t.Fatal(err)
	}

	for path, age := range files {
		_, err := os.Stat(path)
		if old := age > 30*24*time.Hour; old != errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s (%s) : présent = %v", filepath.Base(path), age, err == nil)
		}
	}
	if _, err := os.Stat(filepath.Join(transcripts, "pass-20260901T020000Z")); err == nil {
		t.Error("dossier de transcripts vidé laissé en place")
	}
}

func TestScheduleTriggersAPassAtTheStatedTime(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	svc, c, _, _ := service(t, func(cfg *control.Config) { cfg.Schedule = sched })
	if got := svc.Status().NextSchedule; !got.Equal(time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("prochaine échéance = %s", got)
	}

	if err := svc.Tick(time.Date(2026, 10, 27, 1, 59, 59, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if svc.Status().Queued != nil {
		t.Fatal("passe demandée avant l'heure")
	}
	if err := svc.Tick(time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cl, ok := claim(t, c)
	if !ok || cl.Trigger != relay.Schedule {
		t.Fatalf("passe à l'échéance = %+v, %v", cl, ok)
	}
	finish(t, c, cl.Pass)
	if err := svc.Tick(time.Date(2026, 10, 27, 2, 0, 1, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if svc.Status().Queued != nil {
		t.Fatal("seconde passe demandée pour la même échéance")
	}
	if got := svc.Status().NextSchedule; !got.Equal(time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("échéance suivante = %s", got)
	}
}

func TestManualTriggersMergeIntoOneRequest(t *testing.T) {
	svc, c, _, _ := service(t, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autre-fichier"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := control.Trigger(dir); err != nil {
			t.Fatal(err)
		}
	}

	found, err := svc.TakeRequests(dir)

	if err != nil || !found {
		t.Fatalf("demandes = %v, %v", found, err)
	}
	cl, ok := claim(t, c)
	if !ok || cl.Trigger != relay.Manual {
		t.Fatalf("passe demandée = %+v, %v", cl, ok)
	}
	finish(t, c, cl.Pass)
	if cl, ok := claim(t, c); ok {
		t.Fatalf("passe %s en trop", cl.Pass)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "autre-fichier" {
		t.Fatalf("dossier des demandes = %v, attendu les seules demandes consommées", entries)
	}
}

func TestInvalidEventsAreRefused(t *testing.T) {
	svc, c, _, _ := service(t, nil)
	svc.Request(relay.Manual)
	cl, _ := claim(t, c)
	for _, body := range []string{`{`, `{"stage":"select","type":"merged"}`, `{"type":"started"}`} {
		resp, err := http.Post(c.BaseURL+relay.EventsPath(cl.Pass), "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s : %d, attendu 400", body, resp.StatusCode)
		}
	}
}
