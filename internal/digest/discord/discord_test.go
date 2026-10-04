package discord_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/digest/discord"
)

const secretPath = "/api/webhooks/123/secret-webhook-token"

func TestNotifyPostsContentWithoutMentions(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != secretPath || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("requête inattendue : %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := discord.New(srv.URL+secretPath).Notify(context.Background(), "passe terminée @everyone"); err != nil {
		t.Fatal(err)
	}

	if got["content"] != "passe terminée @everyone" {
		t.Fatalf("content = %v", got["content"])
	}
	mentions, _ := got["allowed_mentions"].(map[string]any)
	if parse, ok := mentions["parse"].([]any); !ok || len(parse) != 0 {
		t.Fatalf("allowed_mentions = %v, attendu aucune mention permise", got["allowed_mentions"])
	}
}

func TestNotifyErrorNeverRevealsTheWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom "+r.URL.Path, http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := discord.New(srv.URL+secretPath).Notify(context.Background(), "x")

	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("erreur = %v, attendu le statut HTTP", err)
	}
	if strings.Contains(err.Error(), "secret-webhook-token") {
		t.Fatalf("l'erreur révèle le webhook : %v", err)
	}
}

func TestNotifyTransportErrorNeverRevealsTheWebhook(t *testing.T) {
	err := discord.New("http://127.0.0.1:1"+secretPath).Notify(context.Background(), "x")

	if err == nil || strings.Contains(err.Error(), "secret-webhook-token") {
		t.Fatalf("erreur = %v, attendue et sans le webhook", err)
	}
}
