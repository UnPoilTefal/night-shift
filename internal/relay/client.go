package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/pass"
)

// ClaimPath est la route où select réclame une passe, en attente longue :
// 200 et un Claim, ou 204 si aucune passe n'a été demandée pendant
// l'attente (paramètre wait). Il n'y a jamais plus d'une passe active.
const ClaimPath = "/v1/passes/claim"

// EventsPath est la route où une étape de confiance rapporte un événement
// de la passe : 204 reçu, 404 passe inconnue, 409 passe déjà close.
func EventsPath(passID string) string {
	return "/v1/passes/" + url.PathEscape(passID) + "/events"
}

// Claim est la passe réclamée par select.
type Claim struct {
	Pass string `json:"pass"`
	// Trigger est le déclencheur de la passe : "schedule" ou "manual".
	Trigger   string    `json:"trigger"`
	Requested time.Time `json:"requested"`
}

// Déclencheurs d'une passe.
const (
	Schedule = "schedule"
	Manual   = "manual"
)

// EventType est la nature d'un événement.
type EventType string

// Événements d'une passe.
const (
	// Started : l'étape commence son tour.
	Started EventType = "started"
	// Finished : l'étape a fini son tour, avec son code de sortie (Exit).
	Finished EventType = "finished"
	// Incident : le détail d'un incident (Detail), qui ne va jamais au
	// digest et reste dans l'historique de l'hôte.
	Incident EventType = "incident"
	// PassFinished : le rapport complet (Report), sous la forme de
	// state/report.json ; l'étape qui publie le digest l'envoie, ce qui
	// clôt la passe.
	PassFinished EventType = "pass-finished"
)

// Event est un événement d'une passe.
type Event struct {
	Stage  string       `json:"stage"`
	Type   EventType    `json:"type"`
	At     time.Time    `json:"at"`
	Exit   *int         `json:"exit,omitempty"`
	Detail string       `json:"detail,omitempty"`
	Report *pass.Report `json:"report,omitempty"`
}

// Erreurs rendues par Send.
var (
	ErrUnknownPass = errors.New("passe inconnue du service de contrôle")
	ErrPassClosed  = errors.New("passe déjà close par le service de contrôle")
)

// Client parle au service de contrôle. Un échec d'envoi n'arrête jamais
// une étape : elle le journalise, et le chien de garde clora la passe.
type Client struct {
	// BaseURL est l'adresse du service, par exemple http://control:8080.
	BaseURL string
	// HTTP est le client HTTP (http.DefaultClient s'il est nul).
	HTTP *http.Client
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Claim réclame une passe, en attendant au plus wait ; ok vaut false si
// aucune n'a été demandée pendant l'attente, et le client reboucle.
func (c Client) Claim(ctx context.Context, wait time.Duration) (cl Claim, ok bool, err error) {
	u := strings.TrimSuffix(c.BaseURL, "/") + ClaimPath + "?wait=" + url.QueryEscape(wait.String())
	resp, err := c.do(ctx, u, nil)
	if err != nil {
		return cl, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return cl, false, nil
	case http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cl); err != nil {
			return cl, false, fmt.Errorf("réponse du service de contrôle illisible : %w", err)
		}
		if cl.Pass == "" {
			return cl, false, errors.New("le service de contrôle a rendu une passe sans identifiant")
		}
		return cl, true, nil
	}
	return cl, false, fmt.Errorf("service de contrôle : %s", resp.Status)
}

// Send rapporte un événement de la passe passID.
func (c Client) Send(ctx context.Context, passID string, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, strings.TrimSuffix(c.BaseURL, "/")+EventsPath(passID), b)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrUnknownPass
	case http.StatusConflict:
		return ErrPassClosed
	}
	return fmt.Errorf("service de contrôle : %s", resp.Status)
}

func (c Client) do(ctx context.Context, u string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http().Do(req) // #nosec G704 -- adresse du service de contrôle, fixée par le déploiement
}
