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

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/gitrepo"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/harness/claude"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/publication"
	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

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
	fs.Var(&repos, "repo", repoUsage())
	ff := addForgeFlags(fs, true)
	d := stageFlags(fs)
	var next stage.Dirs
	fs.StringVar(&next.State, "next-state", "", "en relais : volume de la tâche du tour de relance, vidé avant chaque passe")
	fs.StringVar(&next.Work, "next-work", "", "en relais : volume de travail du tour de relance, vidé avant chaque passe")
	rf := addRelayFlags(fs, true)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(repos) == 0 || d.State == "" || d.Work == "" {
		_, _ = fmt.Fprintln(stderr, "select : au moins un --repo, --state et --work sont requis")
		return 2
	}
	if !rf.valid("select", stderr) {
		return 2
	}
	f, token, ok := ff.open("select", stderr, repos...)
	if !ok {
		return 2
	}

	ctx, stop := stageContext()
	defer stop()
	once := func(ctx context.Context, passID string) (pass.Report, bool, int) {
		return selectOnce(ctx, f, token, ff.name, repos, *d, passID, stdout, stderr)
	}
	if !rf.on {
		_, _, code := once(ctx, "pass-"+time.Now().UTC().Format("20060102T150405Z"))
		return code
	}
	rep := &reporter{client: relay.Client{BaseURL: rf.control}, stage: "select", stderr: stderr}
	// Le marqueur de tour part en premier : l'agent cesse de voir la passe
	// précédente avant que son clone ne disparaisse.
	clean := []string{d.State, d.Work, next.State, next.Work}
	return relaySelect(ctx, rf, rep, *d, clean, once)
}

// selectOnce fait un tour de sélection pour la passe passID : il réserve au
// plus un ticket, prépare son clone et écrit la tâche et le rapport. Il rend
// le rapport, si un ticket est confié à l'agent, et le code de sortie.
func selectOnce(ctx context.Context, f Forge, token, forgeName string, repos []string, d stage.Dirs, passID string, stdout, stderr io.Writer) (pass.Report, bool, int) {
	cfg := pass.Config{Repos: repos, MaxTickets: 1, ID: passID, Now: time.Now}
	report, tasks, err := pass.Select(ctx, cfg, f, f)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	reserved := false
	for _, task := range tasks {
		task.Forge = forgeName
		perr := prepare(ctx, d, f, token, &task)
		if perr == nil {
			reserved = true
			continue
		}
		report.Errors = append(report.Errors, "préparation du clone : "+perr.Error())
		tr, serr := pass.Settle(ctx, cfg, f, f, task, harness.Result{Outcome: harness.Failed, Reason: "préparation du clone : " + perr.Error()})
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
		return report, reserved, 1
	}
	return report, reserved, 0
}

// prepare clone le dépôt cible de la tâche par l'accès git de sa forge de
// code, puis écrit la tâche, complétée du commit de base et de l'identité de
// l'agent.
func prepare(ctx context.Context, d stage.Dirs, code forge.CodeForge, token string, task *harness.Task) error {
	access := code.GitAccess(task.TargetRepo)
	branch, sha, err := gitrepo.Remote{GitAccess: access, Token: token}.Clone(ctx, d.RepoDir())
	if err != nil {
		return err
	}
	task.BaseBranch, task.BaseSHA, task.OptInSHA = branch, sha, sha
	task.AgentIdentity = access.Agent
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
	rf := addRelayFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.State == "" || d.Work == "" || *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "agent : --state, --work et un --timeout positif sont requis")
		return 2
	}
	h := claude.Claude{
		Bin: *bin, Dir: d.RepoDir(), OutDir: d.OutDir(),
		Auth: claude.Auth(*auth), AllowedTools: splitList(*tools), Skill: *skill,
		Timeout: *timeout, Secrets: append(ForgeSecretEnv(), WebhookEnv),
	}
	ctx, stop := stageContext()
	defer stop()
	once := func(ctx context.Context) int { return agentOnce(ctx, *d, h, stdout, stderr) }
	if !rf.on {
		return once(ctx)
	}
	return relayAgent(ctx, *d, once, stdout, stderr)
}

// agentOnce fait un tour de l'agent sur la tâche du volume d, et écrit son
// résultat.
func agentOnce(ctx context.Context, d stage.Dirs, h claude.Claude, stdout, stderr io.Writer) int {
	task, ok, err := d.ReadTask()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agent : %v\n", err)
		return 1
	}
	// Un jeton de forge dans l'environnement, reconnu à son nom ou à sa
	// forme, ou un secret des étapes de confiance donné par un fichier, est
	// une erreur de déploiement : l'agent ne démarre pas, mais un ticket
	// réservé reçoit tout de même un résultat, pour que la Publication le
	// rende à un humain.
	msg := ""
	if k, found := forgeToken(os.Environ()); found {
		msg = fmt.Sprintf("la variable %s porte un jeton de forge ; le conteneur de l'agent ne doit en recevoir aucun (ADR 0005)", k)
	} else if k, found := passSecretFile(os.Environ()); found {
		msg = fmt.Sprintf("la variable %s donne un secret des étapes de confiance ; le conteneur de l'agent ne doit en recevoir aucun (ADR 0005)", k)
	}
	if msg != "" {
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

	// Le moyen d'authentification donné par un fichier passe à l'agent dans
	// la variable que claude attend ; aucune variable …_FILE ne lui parvient.
	env, err := withSecretFiles(os.Environ(), claude.CredentialEnv()...)
	h.BaseSHA, h.Env = task.BaseSHA, env
	var res harness.Result
	if err == nil {
		res, err = h.Run(ctx, task)
	}
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
	transcripts := fs.String("transcripts", "", "dossier d'archive des transcripts, monté par les seules étapes de confiance ; sans lui, aucun transcript n'est conservé")
	followUp := fs.Bool("follow-up", false, "publie un tour de relance : sans tâche, le tour précédent a déjà rendu le ticket et publié le digest")
	ciTimeout := fs.Duration("ci-timeout", pass.DefaultCITimeout, "durée maximale de l'attente de la CI de la PR")
	ciPoll := fs.Duration("ci-poll", pass.DefaultCIPoll, "intervalle entre deux lectures de la CI")
	ciSettle := fs.Duration("ci-settle", pass.DefaultCISettle, "durée pendant laquelle tous les checks doivent rester terminés avant le verdict")
	ciGrace := fs.Duration("ci-grace", pass.DefaultCIGrace, "délai au bout duquel une PR sans aucun check ni statut est rapportée sans CI")
	ff := addForgeFlags(fs, true)
	rf := addRelayFlags(fs, true)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.State == "" || d.Work == "" || (next.State == "") != (next.Work == "") || *ciTimeout <= 0 || *ciPoll <= 0 || *ciSettle <= 0 || *ciGrace <= 0 {
		_, _ = fmt.Fprintln(stderr, "publish : --state et --work sont requis, --next-state et --next-work vont ensemble, et les durées de CI sont positives")
		return 2
	}
	if !rf.valid("publish", stderr) {
		return 2
	}
	f, token, ok := ff.open("publish", stderr)
	if !ok {
		return 2
	}
	hook, ok := webhook("publish", stderr)
	if !ok {
		return 2
	}
	o := publishOpts{
		f: f, token: token, hook: hook, forge: ff.name, next: next, transcripts: *transcripts, followUp: *followUp,
		ci: pass.CIWait{Timeout: *ciTimeout, Poll: *ciPoll, Settle: *ciSettle, Grace: *ciGrace}, relay: rf.on, roundTimeout: rf.roundTimeout,
	}
	ctx, stop := stageContext()
	defer stop()
	once := func(ctx context.Context, d stage.Dirs, pre *harness.Result) (int, *pass.Report, []string) {
		return publishOnce(ctx, o, d, pre, stdout, stderr)
	}
	if !rf.on {
		code, _, _ := once(ctx, *d, nil)
		return code
	}
	name := "publish"
	if *followUp {
		name = "retry-publish"
	}
	return relayPublish(ctx, &reporter{client: relay.Client{BaseURL: rf.control}, stage: name, stderr: stderr}, *d, once)
}

// publishOpts sont les options d'un tour de Publication.
type publishOpts struct {
	f            Forge
	token, forge string
	// hook est l'URL du webhook du digest, lue au démarrage.
	hook        string
	next        stage.Dirs
	transcripts string
	followUp    bool
	ci          pass.CIWait
	// relay fait écrire le marqueur du tour de relance, à échéance de
	// roundTimeout.
	relay        bool
	roundTimeout time.Duration
}

// publishOnce fait un tour de Publication sur les volumes d. pre, s'il est
// donné, remplace le résultat de l'agent (échéance dépassée, sortie
// refusée). Il rend le code de sortie, le rapport de la passe s'il a publié
// son digest, et les incidents de ce tour.
func publishOnce(ctx context.Context, o publishOpts, d stage.Dirs, pre *harness.Result, stdout, stderr io.Writer) (int, *pass.Report, []string) {
	f, token, next := o.f, o.token, o.next
	task, ok, err := d.ReadTask()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "publish : %v\n", err)
		return 1, nil, nil
	}
	// La tâche ne se publie que sur la forge qui l'a réservée : une autre ne
	// connaît ni son ticket ni son dépôt cible.
	if ok && taskForge(task) != o.forge {
		_, _ = fmt.Fprintf(stderr, "publish : la tâche a été réservée sur la forge %s, publish est lancé sur la forge %s\n", taskForge(task), o.forge)
		return 2, nil, nil
	}
	if !ok && o.followUp {
		_, _ = fmt.Fprintln(stdout, "aucune relance : le tour précédent a rendu le ticket")
		return 0, nil, nil
	}
	if !ok {
		_, _ = fmt.Fprintln(stdout, "aucun ticket réservé, rien à publier")
		code, report := publishDigest(ctx, f, o.hook, &d, nil, nil, stdout, stderr)
		return code, report, nil
	}

	var res harness.Result
	if pre != nil {
		res = *pre
	} else {
		res, ok, err = d.ReadResult()
		switch {
		case err != nil:
			res = harness.Result{Outcome: harness.Failed, Reason: "résultat de l'agent refusé : " + err.Error()}
		case !ok:
			res = harness.Result{Outcome: harness.Failed, Reason: "l'agent n'a rendu aucun résultat : passe interrompue (durée maximale ou arrêt du conteneur)"}
		}
	}
	// Le transcript est archivé avant tout rendu ; un échec n'arrête pas la
	// Publication, le digest le compte comme incident.
	var incidents []string
	archived := ""
	if o.transcripts != "" {
		archived, err = d.ArchiveTranscript(res, task, o.transcripts)
		if err != nil {
			incidents = append(incidents, fmt.Sprintf("archive du transcript de %s#%d, tour %d : %v", task.Ticket.Repo, task.Ticket.Number, max(task.Round, 1), err))
			_, _ = fmt.Fprintf(stderr, "publish : %s\n", incidents[len(incidents)-1])
		}
	}
	res.Transcript = archived

	cfg := pass.Config{ID: task.PassID, Now: time.Now, Rounds: 1, CI: o.ci}
	if next.State != "" {
		cfg.Rounds = pass.MaxRounds
	}
	remote := gitrepo.Remote{GitAccess: f.GitAccess(task.TargetRepo), Token: token}
	fu := pass.Follow(ctx, cfg, f, publication.Git{Code: f, Token: token}, task, res)
	if fu.Retry {
		perr := prepareRetry(ctx, d, next, remote, fu.Next, incidents)
		// En relais, le marqueur du tour de relance part en dernier, après
		// la tâche.
		if perr == nil && o.relay {
			perr = relay.WriteTurn(next.State, relay.Turn{
				Pass: task.PassID, Round: fu.Next.Round, Ticket: true, Deadline: time.Now().Add(o.roundTimeout).UTC(),
			})
		}
		if perr == nil {
			_, _ = fmt.Fprintf(stdout, "ticket %s#%d : CI rouge (%d check(s) en échec), relance de l'agent au tour %d\n",
				task.Ticket.Repo, task.Ticket.Number, len(fu.Next.CIFailures), fu.Next.Round)
			return 0, nil, incidents
		}
		// Faute de relance, la CI rouge de ce tour est définitive : Follow en
		// a préparé le rendu.
		_, _ = fmt.Fprintf(stderr, "publish : préparation de la relance : %v\n", perr)
		fu.Result.Reason = "La relance de l'agent n'a pas pu être préparée.\n\n" + fu.Result.Reason
	}

	tr, err := pass.Settle(ctx, cfg, f, f, fu.Task, fu.Result)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "publish : %v\n", err)
		if tr.Repo == "" {
			return 1, nil, incidents
		}
	}
	printTickets(stdout, []pass.TicketReport{tr})
	code, report := publishDigest(ctx, f, o.hook, &d, &tr, incidents, stdout, stderr)
	if code != 0 || err != nil {
		return 1, report, incidents
	}
	return 0, report, incidents
}

// prepareRetry prépare la relance de l'agent dans les volumes next : un
// clone neuf, sans jeton, sur la tête de la branche publiée, la tâche du tour
// suivant et le rapport de la sélection, que la dernière Publication
// complétera pour le digest, avec les incidents de ce tour.
func prepareRetry(ctx context.Context, d, next stage.Dirs, remote gitrepo.Remote, task harness.Task, incidents []string) error {
	if _, _, err := remote.Clone(ctx, next.RepoDir()); err != nil {
		return err
	}
	if _, err := remote.Git(ctx, next.RepoDir(), "checkout", "--quiet", "-b", task.ChangeRequest.Branch, task.BaseSHA); err != nil {
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
		report.Errors = append(report.Errors, incidents...)
		if err := next.WriteReport(report); err != nil {
			return err
		}
	}
	// La tâche en dernier : sans elle, l'agent et la Publication de la
	// relance n'ont rien à faire.
	return next.WriteTask(task)
}

// taskForge rend la forge d'une tâche ; une tâche écrite par une version
// précédente, sans forge, vient de la forge par défaut.
func taskForge(t harness.Task) string {
	if t.Forge == "" {
		return defaultForge
	}
	return t.Forge
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
