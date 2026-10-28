// Package agentconfig lit la configuration de l'agent : l'ensemble
// versionné, distinct de l'image, de ce qui garde l'agent et de ce qu'il lit
// (voir GLOSSARY.md, terme Agent configuration). Une étape de confiance la
// lit dans son dépôt à la dernière révision fusionnée et la remet à l'agent
// en lecture seule. Elle ne peut qu'ajouter des refus aux refus fixes du
// harness, jamais en retirer.
//
// Disposition du dépôt :
//
//	agent.yaml          version et refus ajoutés
//	skills/<nom>/...    une skill par dossier, avec son SKILL.md
package agentconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// File est le fichier de configuration à la racine du dépôt.
const File = "agent.yaml"

// SkillsDir est le dossier des skills à la racine du dépôt.
const SkillsDir = "skills"

// SupportedVersion est la seule version de schéma acceptée.
const SupportedVersion = 1

// Bornes de la configuration : elle passe par le volume de la tâche.
const (
	MaxFiles = 500
	MaxBytes = 10 << 20
)

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Config est la configuration validée de l'agent.
type Config struct {
	Version int `yaml:"version"`
	// DeniedTools s'ajoutent aux refus fixes du harness, au format de
	// --disallowedTools (Bash(npm publish*), WebFetch…).
	DeniedTools []string `yaml:"deniedTools"`
}

// Parse décode et valide agent.yaml. Un champ inconnu ou une valeur
// invalide est une erreur : une configuration douteuse n'est jamais
// appliquée à moitié.
func Parse(data []byte) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return c, fmt.Errorf("%s vide", File)
		}
		return c, fmt.Errorf("%s illisible : %w", File, err)
	}
	if c.Version != SupportedVersion {
		return c, fmt.Errorf("%s : version %d non prise en charge (attendu %d)", File, c.Version, SupportedVersion)
	}
	for _, t := range c.DeniedTools {
		if strings.TrimSpace(t) != t || t == "" || strings.ContainsAny(t, ",\n") {
			return c, fmt.Errorf("%s : refus %q invalide (non vide, sans virgule ni espace en bordure)", File, t)
		}
	}
	return c, nil
}

// Load lit et valide la configuration du dossier dir : agent.yaml, puis
// chaque skill, qui doit être un dossier au nom simple portant un SKILL.md.
// Seuls des dossiers et des fichiers ordinaires sont acceptés.
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, File)) // #nosec G304 -- dossier fixé par l'appelant
	if err != nil {
		return Config{}, fmt.Errorf("configuration de l'agent : %w", err)
	}
	c, err := Parse(b)
	if err != nil {
		return c, fmt.Errorf("configuration de l'agent : %w", err)
	}
	if _, err := walk(dir, nil); err != nil {
		return c, fmt.Errorf("configuration de l'agent : %w", err)
	}
	return c, nil
}

// Skills rend le dossier des skills de dir, ou "" s'il n'en a pas.
func Skills(dir string) string {
	p := filepath.Join(dir, SkillsDir)
	if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
		return ""
	}
	return p
}

// Copy valide la configuration de src puis en recopie agent.yaml et les
// skills dans dst, et rien d'autre du dépôt.
func Copy(src, dst string) error {
	if _, err := Load(src); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil { // #nosec G301 -- lu par l'agent, sans secret
		return err
	}
	_, err := walk(src, func(rel string, d fs.DirEntry) error {
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755) // #nosec G301 -- idem
		}
		return copyFile(filepath.Join(src, rel), to)
	})
	return err
}

// CopySkills recopie les skills de la configuration dir dans dst, le dossier
// des skills de Claude Code de l'agent.
func CopySkills(dir, dst string) error {
	src := Skills(dir)
	if src == "" {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755) // #nosec G301 -- dossier de configuration de l'agent
		case d.Type().IsRegular():
			return copyFile(p, to)
		default:
			return fmt.Errorf("%s n'est pas un fichier ordinaire", rel)
		}
	})
}

// walk parcourt agent.yaml et les skills de dir, refuse tout ce qui n'est ni
// un dossier ni un fichier ordinaire, applique les bornes et appelle visit
// sur chaque entrée retenue, chemin relatif à dir.
func walk(dir string, visit func(rel string, d fs.DirEntry) error) (int64, error) {
	var files int
	var total int64
	count := func(rel string, d fs.DirEntry) error {
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s n'est pas un fichier ordinaire", rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if files++; files > MaxFiles {
			return fmt.Errorf("plus de %d fichiers", MaxFiles)
		}
		if total += fi.Size(); total > MaxBytes {
			return fmt.Errorf("plus de %d octets", MaxBytes)
		}
		return nil
	}
	fi, err := os.Lstat(filepath.Join(dir, File))
	if err != nil {
		return 0, err
	}
	if err := count(File, fs.FileInfoToDirEntry(fi)); err != nil {
		return 0, err
	}
	if visit != nil {
		if err := visit(File, fs.FileInfoToDirEntry(fi)); err != nil {
			return 0, err
		}
	}
	root := filepath.Join(dir, SkillsDir)
	switch fi, err := os.Lstat(root); {
	case errors.Is(err, os.ErrNotExist):
		return total, nil
	case err != nil:
		return 0, err
	case !fi.IsDir():
		return 0, fmt.Errorf("%s n'est pas un dossier", SkillsDir)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch {
		case len(parts) == 2 && !d.IsDir():
			return fmt.Errorf("%s : une skill est un dossier", rel)
		case len(parts) == 2 && !skillName.MatchString(parts[1]):
			return fmt.Errorf("%s : nom de skill invalide (minuscules, chiffres et tirets)", rel)
		case len(parts) == 2:
			if sk, err := os.Lstat(filepath.Join(p, "SKILL.md")); err != nil || !sk.Mode().IsRegular() {
				return fmt.Errorf("%s : SKILL.md manquant", rel)
			}
		case d.IsDir():
		default:
			if err := count(rel, d); err != nil {
				return err
			}
		}
		if visit != nil {
			return visit(rel, d)
		}
		return nil
	})
	return total, err
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src) // #nosec G304 -- chemin validé par walk
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644) // #nosec G306 G703 -- chemin validé par walk, lu par l'agent, sans secret
}
