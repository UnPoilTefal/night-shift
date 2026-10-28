package agentconfig_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/agentconfig"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func repo(t *testing.T) string {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		agentconfig.File:             "version: 1\ndeniedTools:\n  - Bash(npm publish*)\n  - WebFetch\n",
		"skills/review/SKILL.md":     "---\nname: review\n---\n",
		"skills/review/ref/notes.md": "notes",
		"README.md":                  "hors configuration",
	})
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCopyKeepsOnlyTheConfigurationAndItsSkills(t *testing.T) {
	src, dst := repo(t), filepath.Join(t.TempDir(), "agent-config")

	if err := agentconfig.Copy(src, dst); err != nil {
		t.Fatal(err)
	}

	c, err := agentconfig.Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.DeniedTools, []string{"Bash(npm publish*)", "WebFetch"}) {
		t.Fatalf("refus = %v", c.DeniedTools)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "skills/review/ref/notes.md")); err != nil || string(b) != "notes" {
		t.Fatalf("fichier de skill = %q, %v", b, err)
	}
	for _, absent := range []string{"README.md", ".git"} {
		if _, err := os.Stat(filepath.Join(dst, absent)); !os.IsNotExist(err) {
			t.Fatalf("%s recopié : %v", absent, err)
		}
	}
}

func TestCopySkillsInstallsThemForClaudeCode(t *testing.T) {
	src := repo(t)
	dst := filepath.Join(t.TempDir(), "skills")

	if err := agentconfig.CopySkills(src, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "review", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationWithoutSkillsIsValid(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{agentconfig.File: "version: 1\n"})

	if _, err := agentconfig.Load(dir); err != nil {
		t.Fatal(err)
	}
	if err := agentconfig.CopySkills(dir, filepath.Join(t.TempDir(), "skills")); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidConfigurationsAreRefused(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"sans agent.yaml":       {"skills/a/SKILL.md": "x"},
		"version inconnue":      {agentconfig.File: "version: 2\n"},
		"champ inconnu":         {agentconfig.File: "version: 1\nallowedTools: [Bash]\n"},
		"refus avec virgule":    {agentconfig.File: "version: 1\ndeniedTools: ['Bash(a),Bash(b)']\n"},
		"refus vide":            {agentconfig.File: "version: 1\ndeniedTools: ['']\n"},
		"skill sans SKILL.md":   {agentconfig.File: "version: 1\n", "skills/a/notes.md": "x"},
		"fichier hors dossier":  {agentconfig.File: "version: 1\n", "skills/a.md": "x"},
		"nom de skill invalide": {agentconfig.File: "version: 1\n", "skills/Mon Skill/SKILL.md": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, files)
			if _, err := agentconfig.Load(dir); err == nil {
				t.Fatal("configuration acceptée")
			}
		})
	}
}

func TestLinksAreRefusedWithoutTheirContent(t *testing.T) {
	dir := repo(t)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "skills/review/ref/token")); err != nil {
		t.Fatal(err)
	}

	err := agentconfig.Copy(dir, filepath.Join(t.TempDir(), "out"))
	if err == nil || strings.Contains(err.Error(), "ghp_secret") {
		t.Fatalf("erreur = %v, attendu un refus du lien sans son contenu", err)
	}
}
