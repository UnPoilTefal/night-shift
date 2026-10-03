// Package optin lit et valide l'adhésion d'un dépôt cible : le fichier
// versionné par lequel l'équipe propriétaire rend le dépôt éligible aux passes
// et en fixe le périmètre (voir CONTEXT.md, terme Opt-in).
package optin

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

// Path est l'emplacement de l'adhésion dans un dépôt cible.
const Path = ".night-shift/opt-in.yaml"

// ImplicitForbiddenZone couvre la configuration night-shift du dépôt, adhésion
// comprise. Elle s'ajoute toujours aux zones interdites déclarées et ne peut
// pas être retirée.
const ImplicitForbiddenZone = ".night-shift/**"

// SupportedVersion est la seule version de schéma acceptée.
const SupportedVersion = 1

// PullRequestMode indique si les PR d'agent sont ouvertes en brouillon ou
// prêtes pour la relecture.
type PullRequestMode string

// Modes de PR acceptés.
const (
	Draft PullRequestMode = "draft"
	Ready PullRequestMode = "ready"
)

// SensitiveKind précise comment une zone sensible est analysée.
type SensitiveKind string

// Types de zone sensible acceptés.
const (
	// Generic signale toute modification d'un chemin de la zone.
	Generic SensitiveKind = "generic"
	// GoDependencies ne signale, dans un go.mod, que l'ajout d'une dépendance
	// directe ou d'une directive replace ; une montée de version passe.
	GoDependencies SensitiveKind = "go-dependencies"
)

// OptIn est l'adhésion validée d'un dépôt cible.
type OptIn struct {
	Version        int             `yaml:"version"`
	TrustLevel     TrustLevel      `yaml:"trustLevel"`
	ToolImage      string          `yaml:"toolImage"`
	TrustedAuthors []string        `yaml:"trustedAuthors"`
	ForbiddenZones []string        `yaml:"forbiddenZones"`
	SensitiveZones []SensitiveZone `yaml:"sensitiveZones"`
}

// TrustLevel est le palier de confiance accordé aux passes du dépôt.
type TrustLevel struct {
	TicketsPerPass int             `yaml:"ticketsPerPass"`
	PullRequests   PullRequestMode `yaml:"pullRequests"`
}

// SensitiveZone est un motif de chemins dont la modification est signalée.
type SensitiveZone struct {
	Path string        `yaml:"path"`
	Kind SensitiveKind `yaml:"kind"`
}

// Forbidden rend les zones interdites effectives : l'implicite, puis les
// déclarées.
func (o OptIn) Forbidden() []string {
	return append([]string{ImplicitForbiddenZone}, o.ForbiddenZones...)
}

// Parse décode et valide une adhésion. Un champ inconnu, une valeur manquante
// ou un motif invalide est une erreur : on ne devine jamais un périmètre.
func Parse(data []byte) (OptIn, error) {
	var o OptIn
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil {
		return OptIn{}, fmt.Errorf("adhésion illisible : %w", err)
	}
	for i := range o.SensitiveZones {
		if o.SensitiveZones[i].Kind == "" {
			o.SensitiveZones[i].Kind = Generic
		}
	}
	if err := o.validate(); err != nil {
		return OptIn{}, fmt.Errorf("adhésion invalide : %w", err)
	}
	return o, nil
}

func (o OptIn) validate() error {
	var errs []error
	if o.Version != SupportedVersion {
		errs = append(errs, fmt.Errorf("version %d non prise en charge (attendu %d)", o.Version, SupportedVersion))
	}
	if o.TrustLevel.TicketsPerPass < 1 {
		errs = append(errs, errors.New("trustLevel.ticketsPerPass doit valoir au moins 1"))
	}
	switch o.TrustLevel.PullRequests {
	case Draft, Ready:
	default:
		errs = append(errs, fmt.Errorf("trustLevel.pullRequests %q invalide (draft ou ready)", o.TrustLevel.PullRequests))
	}
	for _, a := range o.TrustedAuthors {
		if strings.TrimSpace(a) == "" {
			errs = append(errs, errors.New("trustedAuthors contient un auteur vide"))
		}
	}
	for _, p := range o.ForbiddenZones {
		errs = append(errs, validatePattern("forbiddenZones", p))
	}
	for _, z := range o.SensitiveZones {
		errs = append(errs, validatePattern("sensitiveZones", z.Path))
		switch z.Kind {
		case Generic, GoDependencies:
		default:
			errs = append(errs, fmt.Errorf("sensitiveZones %q : kind %q invalide (generic ou go-dependencies)", z.Path, z.Kind))
		}
	}
	return errors.Join(errs...)
}

func validatePattern(field, p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%s contient un motif vide", field)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s %q : motif absolu, attendu un chemin relatif à la racine du dépôt", field, p)
	}
	if !doublestar.ValidatePattern(p) {
		return fmt.Errorf("%s %q : motif invalide", field, p)
	}
	return nil
}
