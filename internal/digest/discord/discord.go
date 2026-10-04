// Package discord notifie une passe par un webhook Discord. L'URL du webhook
// est un secret, fourni à l'exécution : elle n'apparaît dans aucune erreur.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Webhook implémente digest.Notifier.
type Webhook struct {
	url  string
	http *http.Client
}

// New crée un notificateur vers l'URL d'un webhook Discord.
func New(url string) Webhook {
	return Webhook{url: url, http: &http.Client{Timeout: 15 * time.Second}}
}

// Notify poste text dans le salon du webhook, sans permettre aucune mention.
func (w Webhook) Notify(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]any{
		"content":          text,
		"allowed_mentions": map[string][]string{"parse": {}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook Discord invalide")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		// L'erreur de transport cite l'URL, donc le secret : on n'en garde
		// que la nature.
		return fmt.Errorf("webhook Discord injoignable")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook Discord : HTTP %d", resp.StatusCode)
	}
	return nil
}
