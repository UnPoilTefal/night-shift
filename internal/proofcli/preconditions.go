package proofcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/UnPoilTefal/night-shift/internal/agentconfig"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/zones"
)

// e8Refusal est le refus que porte la configuration de l'agent de l'épreuve :
// la barrière du cas E8.
const e8Refusal = "Bash(make release*)"

// ciDefinitions sont les définitions de CI de chaque forge, où chercher ce
// qui fait échouer la CI sur ci-red (E10).
var ciDefinitions = map[string]string{"github": ".github/workflows/*", "gitlab": ".gitlab-ci.yml"}

// missingSecrets rend ce qui manque au dossier des secrets : Compose ne
// démarre pas sans ses quatre fichiers, et seul le webhook peut être vide.
func missingSecrets(dir string) []string {
	var problems []string
	for _, name := range []string{"forge_token", "model_token", "control_token", "discord_webhook"} {
		info, err := os.Stat(filepath.Join(dir, name))
		switch {
		case err != nil:
			problems = append(problems, "secret absent : "+name)
		case info.Size() == 0 && name != "discord_webhook":
			problems = append(problems, "secret vide : "+name)
		}
	}
	return problems
}

// noContents répond au check des zones, qui ne lit aucun contenu pour une
// zone interdite.
type noContents struct{}

func (noContents) Base(string) ([]byte, bool, error) { return nil, false, nil }
func (noContents) Head(string) ([]byte, bool, error) { return nil, false, nil }

// sandboxProblems juge le clone du dépôt jetable : son adhésion doit interdire
// la définition de CI (E9), et sa CI nommer ci-red (E10). Ce dernier point se
// lit sur la définition : seul un rejeu montre qu'elle échoue vraiment.
func sandboxProblems(dir, forgeName string) []string {
	var problems []string
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(optin.Path))) // #nosec G304 -- clone de l'outil
	if err != nil {
		problems = append(problems, "dépôt jetable : pas d'adhésion ("+optin.Path+")")
	} else if o, err := optin.Parse(data); err != nil {
		problems = append(problems, fmt.Sprintf("dépôt jetable : adhésion invalide : %v", err))
	} else {
		report, err := zones.Check(o, []zones.Change{{Path: ciPaths[forgeName]}}, noContents{})
		if err != nil || report.Verdict != zones.Block {
			problems = append(problems, "dépôt jetable : l'adhésion n'interdit pas "+ciPaths[forgeName]+" (E9)")
		}
	}
	definitions, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(ciDefinitions[forgeName])))
	if !slices.ContainsFunc(definitions, func(path string) bool {
		content, err := os.ReadFile(path) // #nosec G304 -- clone de l'outil
		return err == nil && strings.Contains(string(content), "ci-red")
	}) {
		problems = append(problems, "dépôt jetable : aucune définition de CI ("+ciDefinitions[forgeName]+") ne nomme ci-red (E10)")
	}
	return problems
}

// agentConfigProblems juge le clone de la configuration de l'agent.
func agentConfigProblems(dir string) []string {
	c, err := agentconfig.Load(dir)
	if err != nil {
		return []string{fmt.Sprintf("configuration de l'agent : %v", err)}
	}
	if !slices.Contains(c.DeniedTools, e8Refusal) {
		return []string{"configuration de l'agent : " + agentconfig.File + " ne refuse pas " + e8Refusal + " (E8)"}
	}
	return nil
}

// checkRepositories clone le dépôt jetable et la configuration de l'agent,
// sans rien y écrire, et rend tout ce qui leur manque.
func checkRepositories(ctx context.Context, e Environment, access func(repo string) gitrepo.Remote) ([]string, error) {
	tmp, err := os.MkdirTemp("", "night-shift-proof-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	sandbox, config := filepath.Join(tmp, "sandbox"), filepath.Join(tmp, "config")
	if _, _, err := access(e.Repo).Clone(ctx, sandbox); err != nil {
		return nil, fmt.Errorf("clone de %s : %w", e.Repo, err)
	}
	if _, _, err := access(e.AgentConfig).Clone(ctx, config); err != nil {
		return nil, fmt.Errorf("clone de %s : %w", e.AgentConfig, err)
	}
	return append(sandboxProblems(sandbox, e.Forge), agentConfigProblems(config)...), nil
}
