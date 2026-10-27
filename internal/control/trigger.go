package control

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/relay"
)

// DefaultRequests est le dossier des demandes manuelles : un tmpfs du seul
// conteneur control. control trigger, lancé dans ce conteneur (docker
// compose exec), y dépose une demande ; le service la consomme. Il n'y a ni
// réseau ni secret en jeu.
const DefaultRequests = "/run/night-shift/requests"

// requestPrefix préfixe le nom d'une demande, pour ne jamais consommer un
// fichier qui n'en est pas une.
const requestPrefix = "pass-request-"

// Trigger dépose une demande de passe dans dir.
func Trigger(dir string) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, requestPrefix+hex.EncodeToString(b[:])), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- nom construit ici
	if err != nil {
		return err
	}
	return f.Close()
}

// TakeRequests consomme les demandes déposées dans dir et les fond en une
// seule demande de passe : il rend true s'il y en avait au moins une.
func (s *Service) TakeRequests(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), requestPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return found, err
		}
		found = true
	}
	if found {
		s.Request(relay.Manual)
	}
	return found, nil
}
