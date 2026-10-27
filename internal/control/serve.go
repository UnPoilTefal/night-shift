package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// Listeners sont les adresses que sert le service : l'API des étapes, sur
// control-net, et l'API d'administration, faite pour n'être publiée que sur
// 127.0.0.1 de l'hôte. Admin peut être nul.
type Listeners struct {
	Stages, Admin net.Listener
}

// Serve sert les deux API et fait avancer le service chaque seconde (Tick,
// demandes manuelles de requests) jusqu'à l'annulation de ctx. Une erreur de
// purge ou d'historique est journalisée sans arrêter le service.
func (s *Service) Serve(ctx context.Context, l Listeners, token, requests string, log io.Writer) error {
	if err := os.MkdirAll(requests, 0o700); err != nil {
		return fmt.Errorf("dossier des demandes : %w", err)
	}
	servers := []*http.Server{{Handler: s.StagesHandler(), ReadHeaderTimeout: 10 * time.Second}}
	listeners := []net.Listener{l.Stages}
	if l.Admin != nil {
		servers = append(servers, &http.Server{Handler: s.AdminHandler(token), ReadHeaderTimeout: 10 * time.Second})
		listeners = append(listeners, l.Admin)
	}
	errc := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { errc <- srv.Serve(listeners[i]) }()
	}
	defer func() {
		for _, srv := range servers {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(sctx)
			cancel()
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if _, err := s.TakeRequests(requests); err != nil {
			_, _ = fmt.Fprintf(log, "control : demandes manuelles : %v\n", err)
		}
		if err := s.Tick(s.cfg.Now()); err != nil {
			_, _ = fmt.Fprintf(log, "control : %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-tick.C:
		}
	}
}
