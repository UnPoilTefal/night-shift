// Package claude est l'adaptateur de harness qui fait travailler Claude Code
// en headless (claude -p) sur un clone du dépôt cible. Il tourne dans le
// conteneur de l'agent, qui ne détient aucun jeton de forge : l'agent
// commite localement et le harness rend sa série de commits (format-patch).
package claude

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/harness"
)

// Auth choisit le moyen d'authentification auprès du modèle.
type Auth string

// Moyens d'authentification : chacun lit sa propre variable d'environnement.
const (
	// Subscription : jeton d'abonnement (claude setup-token).
	Subscription Auth = "subscription"
	// APIKey : clé d'API.
	APIKey Auth = "api-key"
)

var credentialEnv = map[Auth]string{ // #nosec G101 -- noms de variables, pas des secrets
	Subscription: "CLAUDE_CODE_OAUTH_TOKEN",
	APIKey:       "ANTHROPIC_API_KEY",
}

// ForgeTokenEnv liste les variables qui portent un jeton de forge : aucune
// n'est transmise à l'agent.
var ForgeTokenEnv = []string{"NIGHT_SHIFT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}

// PassSecretEnv liste les autres secrets de la passe, réservés aux étapes de
// confiance : aucun n'est transmis à l'agent.
var PassSecretEnv = []string{"NIGHT_SHIFT_DISCORD_WEBHOOK"}

// DefaultAllowedTools sont les outils permis à l'agent quand rien d'autre
// n'est configuré : lire et éditer le dépôt, et lancer git, go et make.
var DefaultAllowedTools = []string{"Read", "Edit", "Write", "Glob", "Grep", "Skill", "Bash(git *)", "Bash(go *)", "Bash(make *)"}

// NeedsInfoFile est le fichier, hors du clone, où l'agent écrit le motif de
// son arrêt quand il lui manque une information.
const NeedsInfoFile = "needs-info.md"

// Claude fait travailler claude -p sur un clone.
type Claude struct {
	// Bin est l'exécutable de Claude Code ("claude" par défaut).
	Bin string
	// Dir est le clone du dépôt cible ; BaseSHA, le commit sur lequel
	// l'agent commence.
	Dir     string
	BaseSHA string
	// OutDir, hors du clone, reçoit le motif d'un arrêt needs-info.
	OutDir string
	Auth   Auth
	// AllowedTools est la liste explicite des outils permis ;
	// DefaultAllowedTools si elle est vide.
	AllowedTools []string
	// Skill est la skill invoquée sur le brief ("implement" par défaut).
	Skill string
	// Timeout est la durée maximale du travail de l'agent.
	Timeout time.Duration
	// Env est l'environnement de départ (os.Environ() en production) : seul
	// le moyen d'authentification choisi y est conservé, aucun jeton de
	// forge n'est transmis.
	Env []string
}

// output est la sortie JSON de claude -p --output-format json.
type output struct {
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	SessionID  string  `json:"session_id"`
	CostUSD    float64 `json:"total_cost_usd"`
	DurationMS int64   `json:"duration_ms"`
}

// Run implémente harness.Harness.
func (c Claude) Run(ctx context.Context, task harness.Task) (harness.Result, error) {
	env, err := c.env()
	if err != nil {
		return harness.Result{}, err
	}
	needsInfo := filepath.Join(c.OutDir, NeedsInfoFile)
	if err := os.Remove(needsInfo); err != nil && !errors.Is(err, os.ErrNotExist) {
		return harness.Result{}, err
	}
	env = append(env, "NIGHT_SHIFT_NEEDS_INFO="+needsInfo)

	tools := c.AllowedTools
	if len(tools) == 0 {
		tools = DefaultAllowedTools
	}
	bin := cmp.Or(c.Bin, "claude")
	rctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	// Le brief passe sur l'entrée standard, jamais en argument ni par un
	// shell : son contenu n'est interprété par aucune commande.
	cmd := exec.CommandContext(rctx, bin, // #nosec G204 -- exécutable configuré par l'opérateur, sans shell
		"-p",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--allowedTools", strings.Join(tools, ","),
		"--add-dir", c.OutDir,
	)
	cmd.Dir = c.Dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(c.prompt(task, needsInfo, tools))
	// L'agent lance ses propres processus (outil Bash) : à l'échéance, tout
	// son groupe est tué, pas seulement claude.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()

	var out output
	_ = json.Unmarshal(lastLine(stdout.Bytes()), &out)
	res := harness.Result{
		Agent:     "claude",
		CostUSD:   out.CostUSD,
		Duration:  time.Duration(out.DurationMS) * time.Millisecond,
		SessionID: out.SessionID,
	}
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	res.Transcript = c.transcript(env, out.SessionID)

	if errors.Is(rctx.Err(), context.DeadlineExceeded) {
		res.Outcome, res.Reason = harness.Failed, fmt.Sprintf("durée maximale atteinte (%s), agent interrompu", c.Timeout)
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		res.Outcome, res.Reason = harness.Failed, "agent interrompu : "+err.Error()
		return res, nil
	}
	if reason, err := os.ReadFile(needsInfo); err == nil && len(bytes.TrimSpace(reason)) > 0 { // #nosec G304 -- chemin construit par le harness
		res.Outcome, res.Reason = harness.NeedsInfo, strings.TrimSpace(string(reason))
		return res, nil
	}
	if runErr != nil || out.IsError {
		detail := cmp.Or(out.Result, strings.TrimSpace(tail(stderr.String(), 2000)))
		if runErr != nil {
			detail = cmp.Or(detail, runErr.Error())
		}
		res.Outcome, res.Reason = harness.Failed, "l'agent a échoué : "+detail
		return res, nil
	}

	patches, err := c.patches(ctx)
	if err != nil {
		return res, err
	}
	if len(patches) == 0 {
		res.Outcome, res.Reason = harness.Stopped, "l'agent s'est arrêté sans commit : "+out.Result
		return res, nil
	}
	res.Outcome, res.Reason, res.Patches = harness.Succeeded, out.Result, patches
	return res, nil
}

func (c Claude) prompt(task harness.Task, needsInfo string, tools []string) string {
	return "/" + cmp.Or(c.Skill, "implement") + " " +
		"Le brief ci-dessous fait foi : n'utilise pas d'autre contenu du ticket, tu n'as pas accès à la forge. " +
		"Commite ton travail dans le dépôt courant, en commits atomiques, sans pousser. " +
		"Si le brief ne suffit pas, si une précondition n'est pas remplie, ou s'il faudrait ajouter une dépendance que le brief ne nomme pas, " +
		"ne commite rien : écris le motif dans le fichier " + needsInfo + " et arrête-toi.\n\n" +
		"Seuls ces outils te sont permis, tout autre appel est refusé sans recours : " + strings.Join(tools, ", ") + ". " +
		"Modifie les fichiers avec Edit ou Write, jamais par une commande shell. " +
		"Lance une seule commande par appel Bash, sans enchaînement (&&, ;, |) ni redirection : " +
		"par exemple « git add README.md », puis « git commit -m \"…\" » dans un autre appel.\n\n" +
		"Ticket " + task.Ticket.Repo + "#" + fmt.Sprint(task.Ticket.Number) + ", passe " + task.PassID + ".\n\n" +
		task.Brief
}

// env construit l'environnement de l'agent : celui de départ, sans aucun
// jeton de forge ni autre moyen d'authentification que celui choisi, et avec
// une identité git qui attribue ses commits à l'agent.
func (c Claude) env() ([]string, error) {
	name, ok := credentialEnv[c.Auth]
	if !ok {
		return nil, fmt.Errorf("moyen d'authentification %q inconnu, attendu %s ou %s", c.Auth, Subscription, APIKey)
	}
	drop := append(slices.Concat(ForgeTokenEnv, PassSecretEnv), "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL")
	for _, v := range credentialEnv {
		drop = append(drop, v)
	}
	var env []string
	credential := ""
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		if k == name {
			credential = v
		}
		if !slices.Contains(drop, k) {
			env = append(env, kv)
		}
	}
	if credential == "" {
		return nil, fmt.Errorf("la variable %s doit porter le moyen d'authentification %s", name, c.Auth)
	}
	return append(env, name+"="+credential,
		"GIT_AUTHOR_NAME=night-shift agent", "GIT_AUTHOR_EMAIL=night-shift-agent@users.noreply.github.com",
		"GIT_COMMITTER_NAME=night-shift agent", "GIT_COMMITTER_EMAIL=night-shift-agent@users.noreply.github.com",
	), nil
}

// patches rend la série de commits de l'agent depuis BaseSHA.
func (c Claude) patches(ctx context.Context) ([][]byte, error) {
	dir, err := os.MkdirTemp("", "night-shift-patches-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err := (gitrepo.Remote{}).Git(ctx, c.Dir, "format-patch", "--quiet", "--no-signature", "-o", dir, c.BaseSHA+"..HEAD"); err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.patch"))
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var patches [][]byte
	for _, n := range names {
		b, err := os.ReadFile(n) // #nosec G304 -- fichier écrit par git format-patch dans un dossier temporaire
		if err != nil {
			return nil, err
		}
		patches = append(patches, b)
	}
	return patches, nil
}

// transcript retrouve le fichier de la session dans le dossier de
// configuration de Claude Code ; il rend "" s'il est introuvable.
func (c Claude) transcript(env []string, session string) string {
	if session == "" || strings.ContainsAny(session, `/\*?[`) {
		return ""
	}
	dir := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			dir = v
		} else if v, ok := strings.CutPrefix(kv, "HOME="); ok && dir == "" {
			dir = filepath.Join(v, ".claude")
		}
	}
	if dir == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", session+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func lastLine(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[i+1:]
	}
	return b
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
