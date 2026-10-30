package proofcli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/control"
)

const (
	// Project est le nom du projet Compose de l'environnement d'épreuve. Il
	// n'est pas configurable : son down --volumes ne doit jamais pouvoir
	// viser un déploiement d'usage, dont le nom par défaut est night-shift.
	Project = "night-shift-proof"
	// DefaultAdminPort est le port d'administration sur l'hôte : un autre que
	// celui de l'exemple, pour cohabiter avec un déploiement d'usage.
	DefaultAdminPort = "18081"
	// SecretsDir est le dossier des secrets de l'environnement, relatif à la
	// racine du dépôt : jamais celui d'une stack d'usage.
	SecretsDir = "test/proof/secrets" // #nosec G101 -- un chemin, pas un secret
	// imageRepo est l'image que nomme l'exemple Compose.
	imageRepo = "ghcr.io/unpoiltefal/night-shift"
	// farSchedule coupe les passes planifiées : seules les demandes de
	// l'épreuve en déclenchent une.
	farSchedule = "0 0 1 1 *"
	// placeholder remplit les variables requises par compose.yaml quand la
	// commande ne s'en sert pas (down).
	placeholder = "non-renseigne"
)

// Environment est un environnement d'épreuve : l'exemple Compose et sa
// surcharge, sous le projet Project.
type Environment struct {
	// Root est la racine du dépôt, et Secrets le dossier des secrets ; tous
	// deux absolus.
	Root, Secrets string
	// Repo est le dépôt jetable, AgentConfig le dépôt de la configuration de
	// l'agent de l'épreuve.
	Repo, AgentConfig string
	Forge, Instance   string
	// Version est l'étiquette des images.
	Version   string
	AdminPort string
}

// Compose rend la commande docker compose de l'environnement.
func (e Environment) Compose() []string {
	return []string{"docker", "compose", "--project-name", Project,
		"--file", filepath.Join(e.Root, "examples", "compose", "compose.yaml"),
		"--file", filepath.Join(e.Root, "test", "proof", "proof.yaml")}
}

// Env rend l'environnement de docker compose. Toutes les variables que la
// stack interpole y sont fixées, et celles de l'appelant retirées : ni son
// shell ni un .env posé à côté de l'exemple ne décident de ce qui tourne.
func (e Environment) Env() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "NIGHT_SHIFT_") || strings.HasPrefix(kv, "COMPOSE_")
	})
	return append(env,
		"NIGHT_SHIFT_REPO="+orPlaceholder(e.Repo),
		"NIGHT_SHIFT_AGENT_CONFIG="+orPlaceholder(e.AgentConfig),
		"NIGHT_SHIFT_FORGE="+e.Forge,
		"NIGHT_SHIFT_INSTANCE="+e.Instance,
		"NIGHT_SHIFT_VERSION="+orPlaceholder(e.Version),
		"NIGHT_SHIFT_SCHEDULE="+farSchedule,
		"NIGHT_SHIFT_SECRETS_DIR="+e.Secrets,
		"NIGHT_SHIFT_ADMIN_PORT="+e.AdminPort,
	)
}

func orPlaceholder(v string) string {
	if v == "" {
		return placeholder
	}
	return v
}

// Image rend l'image qui lit et écrit les volumes de l'environnement.
func (e Environment) Image() string { return imageRepo + ":" + e.Version }

// AdminURL rend l'adresse de l'API d'administration de control.
func (e Environment) AdminURL() string { return "http://127.0.0.1:" + e.AdminPort }

func (e Environment) run(ctx context.Context, stderr io.Writer, args ...string) error {
	compose := e.Compose()
	cmd := exec.CommandContext(ctx, compose[0], append(compose[1:], args...)...) // #nosec G204 -- arguments fixés par l'outil
	cmd.Env = e.Env()
	cmd.Stdout, cmd.Stderr = stderr, stderr
	return cmd.Run()
}

// Up lance l'environnement et attend son API d'administration.
func (e Environment) Up(ctx context.Context, adminToken string, stderr io.Writer) error {
	if err := e.run(ctx, stderr, "up", "--detach"); err != nil {
		return fmt.Errorf("docker compose up : %w", err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		err := e.adminReady(ctx, adminToken)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("l'API d'administration ne répond pas : %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (e Environment) adminReady(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.AdminURL()+control.AdminPrefix+"/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- adresse locale fixée par l'outil
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("code %d", resp.StatusCode)
	}
	return nil
}

// Down détruit l'environnement : conteneurs, volumes et réseaux du projet,
// rien d'autre, et aucun dépôt.
func (e Environment) Down(ctx context.Context, stderr io.Writer) error {
	if err := e.run(ctx, stderr, "down", "--volumes", "--remove-orphans", "--timeout", "30"); err != nil {
		return fmt.Errorf("docker compose down : %w", err)
	}
	return nil
}

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+`)

// imageVersion rend l'étiquette des images éprouvées : celle d'une image
// explicite, ou celle que déduit le tag exact du checkout. Il n'y a pas de
// valeur par défaut : on ne rejoue jamais une image qu'on n'a pas désignée.
func imageVersion(image string, exactTag func() (string, error)) (string, error) {
	if image != "" {
		version, ok := strings.CutPrefix(image, imageRepo+":")
		if !ok || version == "" {
			return "", fmt.Errorf("--image %s : attendu %s:<étiquette>, l'image que nomme l'exemple Compose", image, imageRepo)
		}
		return version, nil
	}
	tag, err := exactTag()
	if err != nil || !releaseTag.MatchString(tag) {
		return "", fmt.Errorf("le checkout n'est pas sur un tag exact de release : se placer sur le tag éprouvé, ou désigner l'image (--image, PROOF_IMAGE)")
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// gitExactTag rend le tag exact de la tête du dépôt root.
func gitExactTag(root string) func() (string, error) {
	return func() (string, error) {
		out, err := exec.Command("git", "-C", root, "describe", "--exact-match", "--tags", "--match", "v*", "HEAD").Output() // #nosec G204 -- racine du dépôt
		return strings.TrimSpace(string(out)), err
	}
}
