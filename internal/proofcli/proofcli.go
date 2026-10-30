// Package proofcli est le lanceur de l'épreuve des barrières, un outil de
// développement (ADR 0014) : le paquet cli ne doit jamais l'importer.
package proofcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/cli"
	"github.com/UnPoilTefal/night-shift/internal/proof"
	"github.com/UnPoilTefal/night-shift/internal/proof/stack"
	"github.com/UnPoilTefal/night-shift/internal/version"
)

// ciPaths est la définition de CI de chaque forge, que pose le cas E9.
var ciPaths = map[string]string{"github": ".github/workflows/proof.yml", "gitlab": ".gitlab-ci.yml"}

// Run rejoue l'épreuve des barrières (#116) sur une stack Compose déjà
// lancée et sur son dépôt jetable. Code de sortie : 0 si le palier s'ouvre,
// 1 sinon ou en cas d'incident, 2 pour un usage incorrect.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "replay" {
		_, _ = fmt.Fprintln(stderr, "usage : night-shift-proof replay --repo <dépôt jetable> --compose-file <fichier>… [options]")
		return 2
	}
	fs := flag.NewFlagSet("proof replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "dépôt jetable de l'épreuve, celui de la stack ; jamais un dépôt réel")
	ff := cli.AddForgeFlags(fs, true)
	project := fs.String("compose-project", "night-shift", "nom du projet Compose de la stack")
	var files cli.RepoList
	fs.Var(&files, "compose-file", "fichier Compose de la stack, répétable, dans l'ordre de docker compose")
	image := fs.String("image", "ghcr.io/unpoiltefal/night-shift:"+version.Version, "image night-shift qui lit et écrit les volumes de la stack")
	adminURL := fs.String("admin-url", "http://127.0.0.1:8081", "API d'administration de control")
	tier := fs.String("tier", "1", "palier rejoué")
	var only cli.RepoList
	fs.Var(&only, "case", "cas à rejouer (E1, E2′…), répétable ; tous par défaut")
	blocked := fs.String("blocked-host", "example.com", "hôte hors de la liste blanche du proxy de sortie (E5)")
	passTimeout := fs.Duration("pass-timeout", 45*time.Minute, "durée maximale d'une passe")
	report := fs.String("report", "", "fichier où écrire le rapport Markdown, en plus de la sortie standard")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *repo == "" || len(files) == 0 || *passTimeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "proof replay : --repo, au moins un --compose-file et un --pass-timeout positif sont requis")
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
	f, token, ok := ff.Open("proof replay", stderr, *repo)
	if !ok {
		return 2
	}
	sb, ok := f.(stack.Forge)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "proof replay : l'adaptateur %s ne sait pas administrer un dépôt jetable\n", ff.Name())
		return 2
	}
	adminToken, err := cli.SecretEnv(cli.ControlTokenEnv)
	if err != nil || adminToken == "" {
		_, _ = fmt.Fprintf(stderr, "proof replay : %s (ou %s%s) doit porter le jeton de l'API d'administration de control\n", cli.ControlTokenEnv, cli.ControlTokenEnv, cli.FileSuffix)
		return 2
	}

	compose := []string{"docker", "compose", "--project-name", *project}
	for _, file := range files {
		compose = append(compose, "--file", file)
	}
	ctx, stop := cli.SignalContext()
	defer stop()
	if err := stackTargets(ctx, compose, *repo); err != nil {
		_, _ = fmt.Fprintf(stderr, "proof replay : %v\n", err)
		return 2
	}
	t := stack.New(stack.Config{
		Repo: *repo, Forge: sb, Token: token, CIPath: ciPaths[ff.Name()],
		Compose: compose, Project: *project, Image: *image,
		AdminURL: *adminURL, AdminToken: adminToken, BlockedHost: *blocked,
		PassTimeout: *passTimeout, Log: stderr,
	})
	outcomes := proof.Replay(ctx, t, *tier, cases, stderr)
	md := proof.Report(*tier, outcomes)
	_, _ = fmt.Fprint(stdout, md)
	if *report != "" {
		if err := os.WriteFile(*report, []byte(md), 0o600); err != nil {
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

// stackTargets vérifie que la stack passe bien sur le dépôt jetable : son
// select ne doit viser que lui, sans quoi l'épreuve ouvrirait des tickets
// sur un dépôt que les passes ne parcourent pas, ou l'inverse.
func stackTargets(ctx context.Context, compose []string, repo string) error {
	cmd := exec.CommandContext(ctx, compose[0], append(slices.Clone(compose[1:]), "config", "--format", "json")...) // #nosec G204 -- commande fixée par l'opérateur
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("docker compose config : %w", err)
	}
	var cfg struct {
		Services map[string]struct {
			Command []string `json:"command"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return fmt.Errorf("configuration de la stack illisible : %w", err)
	}
	sel, ok := cfg.Services["select"]
	if !ok {
		return fmt.Errorf("la stack n'a pas de service select")
	}
	var targets []string
	for i, a := range sel.Command {
		if a == "--repo" && i+1 < len(sel.Command) {
			targets = append(targets, strings.Split(sel.Command[i+1], ",")...)
		}
	}
	if !slices.Equal(targets, []string{repo}) {
		return fmt.Errorf("le select de la stack vise %v, attendu le seul dépôt jetable %s", targets, repo)
	}
	return nil
}
