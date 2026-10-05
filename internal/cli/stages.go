package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge/github"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/harness/claude"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/publication"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

// DefaultGitURL est la base des adresses de clone de github.com.
const DefaultGitURL = "https://github.com"

func stageFlags(fs *flag.FlagSet) *stage.Dirs {
	d := &stage.Dirs{}
	fs.StringVar(&d.State, "state", "", "volume de la tâche, écrit par select et en lecture seule ailleurs")
	fs.StringVar(&d.Work, "work", "", "volume de travail : clone du dépôt cible et sortie de l'agent")
	return d
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runSelect est la première étape du palier solo : elle réserve au plus un
// ticket, écrit sa tâche et prépare le clone de travail. Le jeton de forge
// ne quitte pas ce conteneur : le clone n'en garde aucune trace.
func runSelect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("select", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var repos repoList
	fs.Var(&repos, "repo", "dépôt candidat propriétaire/nom (répétable, ou liste séparée par des virgules)")
	apiURL := fs.String("api-url", github.DefaultBaseURL, "URL de l'API REST GitHub")
	gitURL := fs.String("git-url", DefaultGitURL, "base des adresses de clone")
	d := stageFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(repos) == 0 || d.State == "" || d.Work == "" {
		_, _ = fmt.Fprintln(stderr, "select : au moins un --repo, --state et --work sont requis")
		return 2
	}
	token, ok := forgeToken("select", stderr)
	if !ok {
		return 2
	}

	ctx, stop := signalContext()
	defer stop()
	now := time.Now
	cfg := pass.Config{Repos: repos, MaxTickets: 1, ID: "pass-" + now().UTC().Format("20060102T150405Z"), Now: now}
	f := github.New(*apiURL, token)
	report, tasks, err := pass.Select(ctx, cfg, f)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	reserved := false
	for _, task := range tasks {
		perr := prepare(ctx, *d, gitrepo.Remote{BaseURL: *gitURL, Token: token}, &task)
		if perr == nil {
			reserved = true
			continue
		}
		report.Errors = append(report.Errors, "préparation du clone : "+perr.Error())
		tr, serr := pass.Settle(ctx, cfg, f, task, harness.Result{Outcome: harness.Failed, Reason: "préparation du clone : " + perr.Error()})
		if serr != nil {
			report.Errors = append(report.Errors, serr.Error())
			continue
		}
		report.Tickets = append(report.Tickets, tr)
	}
	for _, e := range report.Errors {
		_, _ = fmt.Fprintf(stderr, "select : %s\n", e)
	}
	printRepos(stdout, report)
	printTickets(stdout, report.Tickets)
	printRetriaged(stdout, report.Retriaged)
	if reserved {
		_, _ = fmt.Fprintf(stdout, "ticket %s#%d réservé, confié à l'agent\n", tasks[0].Ticket.Repo, tasks[0].Ticket.Number)
	} else {
		_, _ = fmt.Fprintln(stdout, "aucun ticket prêt éligible")
	}
	// Les incidents vont au rapport plutôt qu'au code de sortie : un échec
	// ici arrêterait le pod avant la Publication, et la passe n'aurait pas de
	// digest. La Publication publie le digest, puis échoue à son tour.
	if err := d.WriteReport(report); err != nil {
		_, _ = fmt.Fprintf(stderr, "select : écriture du rapport : %v\n", err)
		return 1
	}
	return 0
}

func prepare(ctx context.Context, d stage.Dirs, remote gitrepo.Remote, task *harness.Task) error {
	branch, sha, err := remote.Clone(ctx, task.Ticket.Repo, d.RepoDir())
	if err != nil {
		return err
	}
	task.BaseBranch, task.BaseSHA = branch, sha
	if err := os.MkdirAll(d.OutDir(), 0o755); err != nil { // #nosec G301 -- écrit par l'agent, lu par la Publication
		return err
	}
	return d.WriteTask(*task)
}

// runAgent est la deuxième étape : elle fait travailler claude -p sur le
// clone et écrit son résultat. Elle refuse de démarrer si un jeton de forge
// est présent dans son environnement, et rend toujours un résultat, même en
// cas d'échec, pour que la Publication puisse rendre le ticket.
func runAgent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	d := stageFlags(fs)
	bin := fs.String("claude", "claude", "exécutable de Claude Code")
	auth := fs.String("auth", string(claude.Subscription), "authentification auprès du modèle : subscription (CLAUDE_CODE_OAUTH_TOKEN) ou api-key (ANTHROPIC_API_KEY)")
	timeout := fs.Duration("timeout", 25*time.Minute, "durée maximale du travail de l'agent")
	tools := fs.String("allowed-tools", strings.Join(claude.DefaultAllowedTools, ","), "outils permis à l'agent, séparés par des virgules")
	skill := fs.String("skill", "implement", "skill invoquée sur le brief")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.State == "" || d.Work == "" || *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "agent : --state, --work et un --timeout positif sont requis")
		return 2
	}

	task, ok, err := d.ReadTask()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agent : %v\n", err)
		return 1
	}
	// Un jeton de forge dans l'environnement est une erreur de déploiement :
	// l'agent ne démarre pas, mais un ticket réservé reçoit tout de même un
	// résultat, pour que la Publication le rende à un humain.
	for _, k := range claude.ForgeTokenEnv {
		if os.Getenv(k) == "" {
			continue
		}
		msg := fmt.Sprintf("la variable %s porte un jeton de forge ; le conteneur de l'agent ne doit en recevoir aucun (ADR 0005)", k)
		_, _ = fmt.Fprintf(stderr, "agent : %s\n", msg)
		if !ok {
			return 2
		}
		if err := d.WriteResult(harness.Result{Outcome: harness.Failed, Reason: "agent non démarré : " + msg}); err != nil {
			_, _ = fmt.Fprintf(stderr, "agent : écriture du résultat : %v\n", err)
			return 1
		}
		return 0
	}
	if !ok {
		_, _ = fmt.Fprintln(stdout, "aucun ticket réservé, rien à faire")
		return 0
	}

	ctx, stop := signalContext()
	defer stop()
	h := claude.Claude{
		Bin: *bin, Dir: d.RepoDir(), BaseSHA: task.BaseSHA, OutDir: d.OutDir(),
		Auth: claude.Auth(*auth), AllowedTools: splitList(*tools), Skill: *skill,
		Timeout: *timeout, Env: os.Environ(),
	}
	res, err := h.Run(ctx, task)
	if err != nil {
		res = harness.Result{Outcome: harness.Failed, Agent: "claude", Reason: err.Error()}
	}
	if err := d.WriteResult(res); err != nil {
		_, _ = fmt.Fprintf(stderr, "agent : écriture du résultat : %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "ticket %s#%d : %s, %d commit(s), transcript %s\n", task.Ticket.Repo, task.Ticket.Number, res.Outcome, len(res.Patches), res.Transcript)
	return 0
}

// runPublish est l'étape de confiance, sans modèle, qui suit chaque tour de
// l'agent : elle publie sa série en PR brouillon, suit la CI et rend le
// ticket dans un état explicite. Avec --next-state et --next-work, une CI
// rouge ne rend pas encore le ticket : publish prépare la relance de l'agent
// dans ces volumes (tâche, clone neuf de la branche publiée, rapport de la
// sélection). Avec --follow-up, elle publie ce tour de relance, s'il a eu
// lieu.
func runPublish(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	d := stageFlags(fs)
	var next stage.Dirs
	fs.StringVar(&next.State, "next-state", "", "volume de la tâche du tour de relance, que publish écrit si la CI échoue")
	fs.StringVar(&next.Work, "next-work", "", "volume de travail du tour de relance")
	followUp := fs.Bool("follow-up", false, "publie un tour de relance : sans tâche, le tour précédent a déjà rendu le ticket et publié le digest")
	ciTimeout := fs.Duration("ci-timeout", pass.DefaultCITimeout, "durée maximale de l'attente de la CI de la PR")
	ciPoll := fs.Duration("ci-poll", pass.DefaultCIPoll, "intervalle entre deux lectures de la CI")
	ciSettle := fs.Duration("ci-settle", pass.DefaultCISettle, "durée pendant laquelle tous les checks doivent rester terminés avant le verdict")
	apiURL := fs.String("api-url", github.DefaultBaseURL, "URL de l'API REST GitHub")
	gitURL := fs.String("git-url", DefaultGitURL, "base des adresses de clone")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.State == "" || d.Work == "" || (next.State == "") != (next.Work == "") || *ciTimeout <= 0 || *ciPoll <= 0 || *ciSettle <= 0 {
		_, _ = fmt.Fprintln(stderr, "publish : --state et --work sont requis, --next-state et --next-work vont ensemble, et les durées de CI sont positives")
		return 2
	}
	token, ok := forgeToken("publish", stderr)
	if !ok {
		return 2
	}
	task, ok, err := d.ReadTask()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "publish : %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	f := github.New(*apiURL, token)
	if !ok && *followUp {
		_, _ = fmt.Fprintln(stdout, "aucune relance : le tour précédent a rendu le ticket")
		return 0
	}
	if !ok {
		_, _ = fmt.Fprintln(stdout, "aucun ticket réservé, rien à publier")
		return publishDigest(ctx, f, d, nil, stdout, stderr)
	}

	res, ok, err := d.ReadResult()
	switch {
	case err != nil:
		res = harness.Result{Outcome: harness.Failed, Reason: "résultat de l'agent refusé : " + err.Error()}
	case !ok:
		res = harness.Result{Outcome: harness.Failed, Reason: "l'agent n'a rendu aucun résultat : passe interrompue (durée maximale ou arrêt du conteneur)"}
	}

	cfg := pass.Config{ID: task.PassID, Now: time.Now, Rounds: 1, CI: pass.CIWait{Timeout: *ciTimeout, Poll: *ciPoll, Settle: *ciSettle}}
	if next.State != "" {
		cfg.Rounds = pass.MaxRounds
	}
	remote := gitrepo.Remote{BaseURL: *gitURL, Token: token}
	fu := pass.Follow(ctx, cfg, f, publication.Git{Forge: f, GitURL: *gitURL, Token: token}, task, res)
	if fu.Retry {
		perr := prepareRetry(ctx, *d, next, remote, fu.Next)
		if perr == nil {
			_, _ = fmt.Fprintf(stdout, "ticket %s#%d : CI rouge (%d check(s) en échec), relance de l'agent au tour %d\n",
				task.Ticket.Repo, task.Ticket.Number, len(fu.Next.CIFailures), fu.Next.Round)
			return 0
		}
		// Faute de relance, la CI rouge de ce tour est définitive : Follow en
		// a préparé le rendu.
		_, _ = fmt.Fprintf(stderr, "publish : préparation de la relance : %v\n", perr)
		fu.Result.Reason = "La relance de l'agent n'a pas pu être préparée.\n\n" + fu.Result.Reason
	}

	tr, err := pass.Settle(ctx, cfg, f, fu.Task, fu.Result)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "publish : %v\n", err)
		if tr.Repo == "" {
			return 1
		}
	}
	printTickets(stdout, []pass.TicketReport{tr})
	if code := publishDigest(ctx, f, d, &tr, stdout, stderr); code != 0 || err != nil {
		return 1
	}
	return 0
}

// prepareRetry prépare la relance de l'agent dans les volumes next : un
// clone neuf, sans jeton, sur la tête de la branche publiée, la tâche du tour
// suivant et le rapport de la sélection, que la dernière Publication
// complétera pour le digest.
func prepareRetry(ctx context.Context, d, next stage.Dirs, remote gitrepo.Remote, task harness.Task) error {
	if _, _, err := remote.Clone(ctx, task.Ticket.Repo, next.RepoDir()); err != nil {
		return err
	}
	if _, err := remote.Git(ctx, next.RepoDir(), "checkout", "--quiet", "-b", task.PullRequest.Branch, task.BaseSHA); err != nil {
		return err
	}
	if err := os.MkdirAll(next.OutDir(), 0o755); err != nil { // #nosec G301 -- écrit par l'agent, lu par la Publication
		return err
	}
	report, ok, err := d.ReadReport()
	if err != nil {
		return err
	}
	if ok {
		if err := next.WriteReport(report); err != nil {
			return err
		}
	}
	// La tâche en dernier : sans elle, l'agent et la Publication de la
	// relance n'ont rien à faire.
	return next.WriteTask(task)
}

func forgeToken(cmd string, stderr io.Writer) (string, bool) {
	token := os.Getenv(TokenEnv)
	if token == "" {
		_, _ = fmt.Fprintf(stderr, "%s : la variable %s doit porter le jeton de forge\n", cmd, TokenEnv)
		return "", false
	}
	return token, true
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
