// Package relaytest est un faux service de contrôle, à état, qui sert les
// routes du relais v1 : les étapes de confiance s'y testent sans le vrai
// service (#109).
package relaytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/relay"
)

// Server est le faux service de contrôle.
type Server struct {
	URL string

	mu      sync.Mutex
	pending []relay.Claim
	known   map[string]bool
	closed  map[string]bool
	events  map[string][]relay.Event
	wake    chan struct{}
}

// New démarre un faux service, arrêté à la fin du test.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{known: map[string]bool{}, closed: map[string]bool{}, events: map[string][]relay.Event{}, wake: make(chan struct{}, 1)}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Request demande une passe, que le prochain Claim rendra.
func (s *Server) Request(passID, trigger string) {
	s.mu.Lock()
	s.pending = append(s.pending, relay.Claim{Pass: passID, Trigger: trigger, Requested: time.Now().UTC()})
	s.known[passID] = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close clôt une passe, comme le ferait le chien de garde.
func (s *Server) Close(passID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed[passID] = true
}

// Events rend les événements reçus pour une passe.
func (s *Server) Events(passID string) []relay.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]relay.Event(nil), s.events[passID]...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "méthode refusée", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == relay.ClaimPath {
		s.claim(w, r)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/v1/passes/")
	id, ok2 := strings.CutSuffix(rest, "/events")
	if !ok || !ok2 || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	passID, err := url.PathUnescape(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var e relay.Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "événement illisible", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.known[passID]:
		http.NotFound(w, r)
		return
	case s.closed[passID]:
		w.WriteHeader(http.StatusConflict)
		return
	}
	s.events[passID] = append(s.events[passID], e)
	if e.Type == relay.PassFinished {
		s.closed[passID] = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	wait, err := time.ParseDuration(r.URL.Query().Get("wait"))
	if err != nil || wait < 0 {
		wait = 0
	}
	deadline := time.After(wait)
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			c := s.pending[0]
			s.pending = s.pending[1:]
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c)
			return
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-deadline:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}
