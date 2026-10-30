package cli

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/stage"
)

// RelayKnobs règle le relais le temps d'un test.
type RelayKnobs struct {
	// Rounds borne les tours traités par une étape avant qu'elle rende la
	// main (1 par défaut).
	Rounds int
	// Timeout borne chaque étape (5 s par défaut).
	Timeout time.Duration
	// Kill remplace le kill -1 de l'agent : un test ne doit jamais signaler
	// les processus de la machine qui le lance.
	Kill func() error
	// RequirePID1 garde l'exigence du PID 1 pour l'agent.
	RequirePID1 bool
	// AfterCopy suit la copie de la sortie de l'agent par publish.
	AfterCopy func(stage.Dirs)
}

// Relay applique k le temps d'un test.
func Relay(t testing.TB, k RelayKnobs) {
	t.Helper()
	prev := []any{relayPoll, claimWait, relayRounds, requirePID1, killOthers, stageContext, afterCopy}
	relayPoll, claimWait = 2*time.Millisecond, 20*time.Millisecond
	relayRounds = max(k.Rounds, 1)
	requirePID1 = k.RequirePID1
	killOthers = func() error { return nil }
	if k.Kill != nil {
		killOthers = k.Kill
	}
	timeout := k.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	stageContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), timeout)
	}
	afterCopy = func(stage.Dirs) {}
	if k.AfterCopy != nil {
		afterCopy = k.AfterCopy
	}
	t.Cleanup(func() {
		relayPoll, claimWait, relayRounds, requirePID1 = prev[0].(time.Duration), prev[1].(time.Duration), prev[2].(int), prev[3].(bool)
		killOthers = prev[4].(func() error)
		stageContext = prev[5].(func() (context.Context, context.CancelFunc))
		afterCopy = prev[6].(func(stage.Dirs))
	})
}

// AddForge inscrit au registre, le temps d'un test, une forge et ses
// adresses par défaut, comme le ferait une nouvelle entrée de forges.
func AddForge(t testing.TB, name, tokenEnv, apiURL, gitURL string, open func(apiURL, gitURL, token string) Forge) {
	t.Helper()
	previous := forges
	forges = append(slices.Clone(forges), forgeAdapter{
		name: name, tokenEnv: tokenEnv, apiURL: apiURL, gitURL: gitURL, open: open,
		instance: func(base string) (string, string) { return base + "/api", base },
		repoForm: "propriétaire/nom", validRepo: func(string) error { return nil },
	})
	t.Cleanup(func() { forges = previous })
}

