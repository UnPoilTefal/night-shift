// Package proofcli crée, rejoue et détruit l'environnement d'épreuve, un
// outil de développement (ADR 0014) : le paquet cli ne doit jamais l'importer.
package proofcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/proof"
	"github.com/UnPoilTefal/night-shift/internal/proof/stack"
)

// ciPaths est la définition de CI de chaque forge, que pose le cas E9.
var ciPaths = map[string]string{"github": ".github/workflows/proof.yml", "gitlab": ".gitlab-ci.yml"}

const usage = `usage : night-shift-proof <commande> (depuis la racine du dépôt)

Commandes :
  up      vérifie les préconditions, puis lance l'environnement d'épreuve
  replay  rejoue l'épreuve dans cet environnement
  down    détruit l'environnement ; aucun dépôt n'est touché
`

// Run aiguille les commandes de l'outil. Code de sortie : 0 en cas de succès
// (pour replay, si le palier s'ouvre), 1 sinon ou en cas d'incident, 2 pour un
// usage incorrect ou une précondition manquante.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "up":
		return runUp(args[1:], stderr)
	case "replay":
		return runReplay(args[1:], stdout, stderr)
	case "down":
		return runDown(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "commande inconnue : %q\n\n%s", args[0], usage)
		return 2
	}
}

// options sont celles qui désignent l'environnement.
type options struct {
	repo, agentConfig, image, adminPort *string
	forge                               cli.ForgeFlags
}

func addOptions(fs *flag.FlagSet) options {
	return options{
		repo:        fs.String("repo", "", "dépôt jetable de l'épreuve ; jamais un dépôt réel"),
		agentConfig: fs.String("agent-config", "", "dépôt de la configuration de l'agent de l'épreuve ; jamais celui d'un déploiement d'usage"),
		image:       fs.String("image", "", "image éprouvée, "+imageRepo+":<étiquette> ; déduite du tag exact du checkout si absente"),
		adminPort:   fs.String("admin-port", DefaultAdminPort, "port de l'API d'administration, sur 127.0.0.1"),
		forge:       cli.AddForgeFlags(fs, true),
	}
}

// root rend la racine du dépôt : l'outil se lance depuis un checkout.
func root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "test", "proof", "proof.yaml")); err != nil {
		return "", fmt.Errorf("à lancer depuis la racine du dépôt night-shift")
	}
	return dir, nil
}

// prepare vérifie tout ce qui peut l'être avant d'écrire quoi que ce soit, et
// signale chaque manque. Le jeton de forge se lit dans le dossier des secrets
// de l'environnement, jamais ailleurs.
func prepare(ctx context.Context, cmd string, o options, stderr io.Writer) (Environment, cli.Forge, string, bool) {
	fail := func(format string, a ...any) (Environment, cli.Forge, string, bool) {
		_, _ = fmt.Fprintf(stderr, "proof %s : "+format+"\n", append([]any{cmd}, a...)...)
		return Environment{}, nil, "", false
	}
	if *o.repo == "" || *o.agentConfig == "" {
		return fail("--repo et --agent-config sont requis")
	}
	if *o.repo == *o.agentConfig {
		return fail("le dépôt jetable et la configuration de l'agent doivent être deux dépôts")
	}
	if os.Getenv("DOCKER_HOST") != "" {
		return fail("DOCKER_HOST est écarté : les fichiers montés seraient cherchés sur l'hôte distant. Se connecter à l'hôte et y lancer la commande (docs/proof.md)")
	}
	dir, err := root()
	if err != nil {
		return fail("%v", err)
	}
	version, err := imageVersion(*o.image, gitExactTag(dir))
	if err != nil {
		return fail("%v", err)
	}
	e := Environment{Root: dir, Secrets: filepath.Join(dir, filepath.FromSlash(SecretsDir)),
		Repo: *o.repo, AgentConfig: *o.agentConfig, Version: version, AdminPort: *o.adminPort,
		Forge: o.forge.Name(), Instance: o.forge.Instance()}
	if missing := missingSecrets(e.Secrets); len(missing) > 0 {
		return report(cmd, append(missing, "dossier des secrets : "+SecretsDir+" (docs/proof.md)"), stderr)
	}
	if err := o.forge.UseTokenFile(filepath.Join(e.Secrets, "forge_token")); err != nil {
		return fail("%v", err)
	}
	f, token, ok := o.forge.Open("proof "+cmd, stderr, e.Repo, e.AgentConfig)
	if !ok {
		return Environment{}, nil, "", false
	}
	problems, err := checkRepositories(ctx, e, func(repo string) gitrepo.Remote {
		return gitrepo.Remote{GitAccess: f.GitAccess(repo), Token: token}
	})
	if err != nil {
		return fail("%v", err)
	}
	if len(problems) > 0 {
		return report(cmd, problems, stderr)
	}
	return e, f, token, true
}

func report(cmd string, problems []string, stderr io.Writer) (Environment, cli.Forge, string, bool) {
	_, _ = fmt.Fprintf(stderr, "proof %s : préconditions non remplies, rien n'a été écrit :\n", cmd)
	for _, p := range problems {
		_, _ = fmt.Fprintf(stderr, "  - %s\n", p)
	}
	return Environment{}, nil, "", false
}

func controlToken(e Environment) (string, error) {
	b, err := os.ReadFile(filepath.Join(e.Secrets, "control_token")) // #nosec G304 -- dossier des secrets de l'environnement
	if err != nil {
		return "", fmt.Errorf("le jeton de l'API d'administration est illisible")
	}
	return strings.TrimSpace(string(b)), nil
}

func runUp(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("proof up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := addOptions(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, stop := cli.SignalContext()
	defer stop()
	e, _, _, ok := prepare(ctx, "up", o, stderr)
	if !ok {
		return 2
	}
	token, err := controlToken(e)
	if err == nil {
		err = e.Up(ctx, token, stderr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proof up : %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "environnement d'épreuve lancé : projet %s, images %s, administration sur %s\n", Project, e.Version, e.AdminURL())
	return 0
}

func runDown(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("proof down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	adminPort := fs.String("admin-port", DefaultAdminPort, "port de l'API d'administration, sur 127.0.0.1")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := root()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proof down : %v\n", err)
		return 2
	}
	ctx, stop := cli.SignalContext()
	defer stop()
	e := Environment{Root: dir, Secrets: filepath.Join(dir, filepath.FromSlash(SecretsDir)), AdminPort: *adminPort}
	if err := e.Down(ctx, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "proof down : %v\n", err)
		return 1
	}
	return 0
}

// runReplay rejoue l'épreuve des barrières (#116) dans l'environnement lancé
// par up. Code de sortie : 0 si le palier s'ouvre, 1 sinon ou en cas
// d'incident, 2 pour un usage incorrect.
func runReplay(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("proof replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := addOptions(fs)
	tier := fs.String("tier", "1", "palier rejoué")
	var only cli.RepoList
	fs.Var(&only, "case", "cas à rejouer (E1, E2′…), répétable ; tous par défaut")
	blocked := fs.String("blocked-host", "example.com", "hôte hors de la liste blanche du proxy de sortie (E5)")
	passTimeout := fs.Duration("pass-timeout", 45*time.Minute, "durée maximale d'une passe")
	reportFile := fs.String("report", "", "fichier où écrire le rapport Markdown, en plus de la sortie standard")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *passTimeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "proof replay : --pass-timeout doit être positif")
		return 2
	}
	cases, err := proof.Cases()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proof replay : %v\n", err)
		return 1
	}
	if len(only) > 0 {
		for _, id := range only {
			if !slices.ContainsFunc(cases, func(c proof.Case) bool { return c.ID == id }) {
				_, _ = fmt.Fprintf(stderr, "proof replay : cas inconnu : %s\n", id)
				return 2
			}
		}
		cases = slices.DeleteFunc(cases, func(c proof.Case) bool { return !slices.Contains(only, c.ID) })
	}
	ctx, stop := cli.SignalContext()
	defer stop()
	e, f, token, ok := prepare(ctx, "replay", o, stderr)
	if !ok {
		return 2
	}
	sb, ok := f.(stack.Forge)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "proof replay : l'adaptateur %s ne sait pas administrer un dépôt jetable\n", e.Forge)
		return 2
	}
	adminToken, err := controlToken(e)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proof replay : %v\n", err)
		return 2
	}
	if err := stackTargets(ctx, e, *o.repo); err != nil {
		_, _ = fmt.Fprintf(stderr, "proof replay : %v\n", err)
		return 2
	}
	t := stack.New(stack.Config{
		Repo: e.Repo, Forge: sb, Token: token, CIPath: ciPaths[e.Forge],
		Compose: e.Compose(), Project: Project, Env: e.Env(), Image: e.Image(),
		AdminURL: e.AdminURL(), AdminToken: adminToken, BlockedHost: *blocked,
		PassTimeout: *passTimeout, Log: stderr,
	})
	outcomes := proof.Replay(ctx, t, *tier, cases, stderr)
	md := proof.Report(*tier, outcomes)
	_, _ = fmt.Fprint(stdout, md)
	if *reportFile != "" {
		if err := os.WriteFile(*reportFile, []byte(md), 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "proof replay : écriture du rapport : %v\n", err)
			return 1
		}
	}
	for _, o := range outcomes {
		if o.Err != nil {
			return 1
		}
	}
	if !proof.Open(outcomes) {
		return 1
	}
	return 0
}

// stackTargets vérifie que l'environnement tourne, et sur ce dépôt jetable :
// c'est le select lancé qui fait foi, pas les options de cette commande.
func stackTargets(ctx context.Context, e Environment, repo string) error {
	compose := e.Compose()
	ps := exec.CommandContext(ctx, compose[0], append(compose[1:], "ps", "--all", "--quiet", "select")...) // #nosec G204 -- arguments fixés par l'outil
	ps.Env = e.Env()
	out, err := ps.Output()
	id := strings.TrimSpace(string(out))
	if err != nil || id == "" {
		return fmt.Errorf("l'environnement d'épreuve ne tourne pas : make proof-up")
	}
	out, err = exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config.Cmd}}", id).Output() // #nosec G204 -- identifiant rendu par Compose
	if err != nil {
		return fmt.Errorf("docker inspect : %w", err)
	}
	var command []string
	if err := json.Unmarshal(out, &command); err != nil {
		return fmt.Errorf("commande de select illisible : %w", err)
	}
	var targets []string
	for i, a := range command {
		if a == "--repo" && i+1 < len(command) {
			targets = append(targets, strings.Split(command[i+1], ",")...)
		}
	}
	if !slices.Equal(targets, []string{repo}) {
		return fmt.Errorf("le select de l'environnement vise %v, attendu le seul dépôt jetable %s : make proof-down, puis make proof-up", targets, repo)
	}
	return nil
}
