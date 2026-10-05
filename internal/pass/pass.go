// Package pass est l'orchestrateur d'une passe : il parcourt la file des
// tickets prêts des dépôts adhérents, en réserve au plus un nombre plafonné,
// les confie au harness, suit la CI de chaque PR publiée en relançant au
// besoin l'agent, et rend chacun dans un état explicite. Il ne connaît
// la forge et l'agent qu'à travers leurs adaptateurs.
package pass

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/optin"
)

// Config paramètre une passe.
type Config struct {
	// Repos liste les dépôts candidats ; seuls ceux qui portent une adhésion
	// valide sont parcourus.
	Repos []string
	// MaxTickets plafonne le nombre de tickets traités par la passe.
	MaxTickets int
	// ID identifie la passe dans les commentaires et le rapport.
	ID string
	// Now est l'horloge de la passe.
	Now func() time.Time
	// Rounds plafonne les tours de l'agent par ticket, jamais au-delà de
	// MaxRounds ; zéro vaut MaxRounds.
	Rounds int
	// CITimeout borne l'attente de la CI d'un tour, CIPoll espace ses
	// lectures ; zéro vaut DefaultCITimeout et DefaultCIPoll.
	CITimeout, CIPoll time.Duration
}

// MaxRounds plafonne les tours de l'agent sur un ticket : un premier
// passage, puis une seule relance si la CI de la PR échoue.
const MaxRounds = 2

// Attente de la CI par défaut.
const (
	DefaultCITimeout = 10 * time.Minute
	DefaultCIPoll    = 30 * time.Second
)

// CIFailed est l'issue d'un ticket dont la CI échoue encore au dernier tour
// permis : la PR reste en brouillon et le ticket revient à un humain. Seule
// la passe la prononce, jamais l'agent.
const CIFailed harness.Outcome = "ci-failed"

// RepoStatus dit si un dépôt candidat a été parcouru.
type RepoStatus string

// Statuts possibles d'un dépôt candidat.
const (
	Eligible     RepoStatus = "eligible"
	NoOptIn      RepoStatus = "no-opt-in"
	InvalidOptIn RepoStatus = "invalid-opt-in"
	Unreachable  RepoStatus = "unreachable"
)

// RepoReport est le constat de la passe sur un dépôt candidat.
type RepoReport struct {
	Repo   string
	Status RepoStatus
	Detail string
}

// TicketReport est le constat de la passe sur un ticket traité.
type TicketReport struct {
	Repo    string
	Number  int
	Title   string
	Outcome harness.Outcome
	Reason  string
	// PullRequest est l'URL de la PR en brouillon, en cas de succès.
	PullRequest string
	CostUSD     float64
	Duration    time.Duration
	Transcript  string
}

// RetriageReport est le constat d'un ticket sorti de la file sans être
// traité, faute d'un brief que seul du contenu de confiance fonde.
type RetriageReport struct {
	Repo   string
	Number int
	Reason string
}

// Report est le rapport d'une passe.
type Report struct {
	ID      string
	Repos   []RepoReport
	Tickets []TicketReport
	// Retriaged liste les tickets repassés en needs-triage.
	Retriaged []RetriageReport
	// Started et Finished bornent la passe, de la sélection au rendu du
	// dernier ticket.
	Started, Finished time.Time
	// Errors sont les incidents qui ont interrompu une partie de la passe ;
	// le digest les rapporte.
	Errors []string
}

// Published est ce qu'a produit la Publication d'un tour : la PR en
// brouillon et le commit poussé en tête de sa branche.
type Published struct {
	Draft forge.Draft
	Head  string
}

// Publisher est l'étape de Publication : elle applique la série de commits
// de l'agent sur une branche agent/, la pousse et ouvre la PR en brouillon.
// Quand la tâche porte déjà une PR (tour de relance), elle pousse sur sa
// branche sans en ouvrir d'autre. C'est la seule étape qui écrit du code sur
// la forge.
type Publisher interface {
	Publish(ctx context.Context, task harness.Task, res harness.Result) (Published, error)
}

// Run exécute une passe complète dans un seul processus : sélection et
// réservation, travail de l'agent, Publication et suivi de la CI, relance de
// l'agent si elle échoue, puis rendu de chaque ticket. Le palier solo répartit
// ces étapes entre plusieurs conteneurs (Select, harness, Follow et Settle)
// pour que l'agent ne côtoie jamais le jeton de forge.
func Run(ctx context.Context, cfg Config, f forge.Forge, h harness.Harness, p Publisher) (Report, error) {
	r, tasks, err := Select(ctx, cfg, f)
	end := func(err error) (Report, error) {
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
		}
		r.Finished = cfg.Now()
		return r, err
	}
	if err != nil {
		for _, t := range tasks {
			if tr, serr := Settle(ctx, cfg, f, t, harness.Result{Outcome: harness.Failed, Reason: "passe interrompue avant le travail de l'agent"}); serr == nil {
				r.Tickets = append(r.Tickets, tr)
			}
		}
		return end(err)
	}
	for _, t := range tasks {
		fu := Followup{Retry: true, Task: t}
		for fu.Retry {
			res, err := h.Run(ctx, fu.Task)
			if err != nil {
				res = harness.Result{Outcome: harness.Failed, Reason: err.Error()}
			}
			fu = Follow(ctx, cfg, f, p, fu.Task, res)
		}
		tr, err := Settle(ctx, cfg, f, fu.Task, fu.Result)
		if tr.Repo != "" {
			r.Tickets = append(r.Tickets, tr)
		}
		if err != nil {
			return end(err)
		}
	}
	return end(nil)
}

// Select parcourt la file des dépôts adhérents et réserve au plus
// cfg.MaxTickets tickets, dans l'ordre priorité puis ancienneté. Chaque tâche
// rendue est réservée et doit être rendue par Settle, y compris quand Select
// rend aussi une erreur. Un ticket dont le brief ne repose pas sur du seul
// contenu de confiance n'est pas réservé : il repasse en needs-triage.
func Select(ctx context.Context, cfg Config, f forge.Forge) (Report, []harness.Task, error) {
	r := Report{ID: cfg.ID, Started: cfg.Now()}
	var queue []forge.Ticket
	perRepoCap := map[string]int{}
	trustedAuthors := map[string][]string{}

	for _, repo := range dedupe(cfg.Repos) {
		o, rr := readOptIn(ctx, f, repo)
		r.Repos = append(r.Repos, rr)
		if rr.Status != Eligible {
			continue
		}
		ts, err := f.ReadyTickets(ctx, repo)
		if err != nil {
			r.Repos[len(r.Repos)-1] = RepoReport{Repo: repo, Status: Unreachable, Detail: err.Error()}
			continue
		}
		perRepoCap[repo] = o.TrustLevel.TicketsPerPass
		trustedAuthors[repo] = o.TrustedAuthors
		for _, t := range ts {
			if t.OpenBlockers == 0 {
				queue = append(queue, t)
			}
		}
	}

	slices.SortStableFunc(queue, func(a, b forge.Ticket) int {
		return cmp.Or(
			cmp.Compare(priority(a), priority(b)),
			a.CreatedAt.Compare(b.CreatedAt),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.Number, b.Number),
		)
	})

	var tasks []harness.Task
	taken := map[string]int{}
	for _, t := range queue {
		if len(tasks) >= cfg.MaxTickets {
			break
		}
		if taken[t.Repo] >= perRepoCap[t.Repo] {
			continue
		}
		brief, reason := Brief(t, trustedAuthors[t.Repo])
		if reason != "" {
			if err := retriage(ctx, cfg, f, t, reason); err != nil {
				return r, tasks, fmt.Errorf("retri de %s#%d : %w", t.Repo, t.Number, err)
			}
			r.Retriaged = append(r.Retriaged, RetriageReport{Repo: t.Repo, Number: t.Number, Reason: reason})
			continue
		}
		if err := reserve(ctx, cfg, f, t); err != nil {
			rollback(ctx, f, t)
			return r, tasks, fmt.Errorf("réservation de %s#%d : %w", t.Repo, t.Number, err)
		}
		taken[t.Repo]++
		tasks = append(tasks, harness.Task{Ticket: relayed(t, trustedAuthors[t.Repo]), Brief: brief, PassID: cfg.ID, Round: 1})
	}
	return r, tasks, nil
}

// Brief construit le brief transmis à l'agent à partir du seul contenu
// d'auteurs de confiance : le titre et le corps si l'auteur du ticket l'est,
// puis les commentaires de confiance dans l'ordre, hors ceux de night-shift.
// Le reste est écarté. Le brief date du dernier contenu de confiance. Si
// aucun contenu n'est de confiance, ou si un tiers a écrit ou modifié un
// commentaire depuis, Brief rend le motif pour lequel le ticket doit être
// retrié par un humain.
func Brief(t forge.Ticket, trustedAuthors []string) (brief, retriage string) {
	var b strings.Builder
	var last time.Time
	if trusted(t.Author, t.AuthorAssociated, trustedAuthors) {
		b.WriteString("# " + t.Title + "\n\n" + t.Body + "\n")
		last = t.CreatedAt
	}
	for _, c := range t.Comments {
		if !trusted(c.Author, c.Associated, trustedAuthors) {
			continue
		}
		last = later(last, c.CreatedAt)
		// Les comptes rendus des passes précédentes datent le brief sans en
		// faire partie.
		if !strings.HasPrefix(c.Body, commentPrefix) {
			fmt.Fprintf(&b, "\n---\n\nCommentaire de %s :\n\n%s\n", c.Author, c.Body)
		}
	}
	if b.Len() == 0 {
		return "", "aucun contenu du ticket n'est écrit par un auteur de confiance"
	}
	for _, c := range t.Comments {
		if !trusted(c.Author, c.Associated, trustedAuthors) && !later(c.CreatedAt, c.UpdatedAt).Before(last) {
			return "", fmt.Sprintf("un commentaire de `%s`, auteur hors confiance, est postérieur au brief", c.Author)
		}
	}
	return b.String(), ""
}

// trusted dit si un auteur est de confiance : associé au dépôt, ou nommé par
// l'adhésion. Les forges ignorent la casse des identifiants.
func trusted(author string, associated bool, trustedAuthors []string) bool {
	return associated || slices.ContainsFunc(trustedAuthors, func(a string) bool { return strings.EqualFold(a, author) })
}

// later rend la plus récente de deux dates.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// relayed rend le ticket tel qu'il est relayé jusqu'au conteneur de
// l'agent : sans corps ni commentaires, et sans titre si son auteur n'est
// pas de confiance. Le brief reste le seul contenu du ticket qu'il reçoit.
func relayed(t forge.Ticket, trustedAuthors []string) forge.Ticket {
	t.Body, t.Comments = "", nil
	if !trusted(t.Author, t.AuthorAssociated, trustedAuthors) {
		t.Title = ""
	}
	return t
}

func readOptIn(ctx context.Context, f forge.Forge, repo string) (optin.OptIn, RepoReport) {
	data, ok, err := f.File(ctx, repo, optin.Path)
	switch {
	case err != nil:
		return optin.OptIn{}, RepoReport{Repo: repo, Status: Unreachable, Detail: err.Error()}
	case !ok:
		return optin.OptIn{}, RepoReport{Repo: repo, Status: NoOptIn}
	}
	o, err := optin.Parse(data)
	if err != nil {
		return optin.OptIn{}, RepoReport{Repo: repo, Status: InvalidOptIn, Detail: err.Error()}
	}
	return o, RepoReport{Repo: repo, Status: Eligible}
}

// priority rend 0 à 3 pour prio:P0 à prio:P3, et 4 sans label de priorité.
func priority(t forge.Ticket) int {
	best := 4
	for _, l := range t.Labels {
		if p, ok := strings.CutPrefix(l, "prio:P"); ok && len(p) == 1 && p[0] >= '0' && p[0] <= '3' {
			best = min(best, int(p[0]-'0'))
		}
	}
	return best
}

// commentPrefix ouvre chaque commentaire que night-shift poste sur un
// ticket.
const commentPrefix = "night-shift "

// cleanupTimeout borne les appels à la forge qui rendent un ticket : ils
// doivent aboutir même si la passe a été annulée ou a dépassé sa durée.
const cleanupTimeout = time.Minute

// Followup est la suite d'un tour de l'agent sur un ticket.
type Followup struct {
	// Retry demande un nouveau tour de l'agent sur Task : la CI de la PR
	// échoue et un tour reste permis.
	Retry bool
	// Task est la tâche du tour suivant si Retry, sinon celle à rendre,
	// complétée de la PR publiée.
	Task harness.Task
	// Result est le résultat à rendre par Settle quand Retry est faux.
	Result harness.Result
}

// Follow publie la série d'un tour réussi, puis suit la CI de la PR jusqu'à
// son verdict ou jusqu'à cfg.CITimeout. Si elle échoue et qu'un tour reste
// permis, Follow rend la tâche de la relance : même brief, tête de la PR
// publiée, checks en échec. Sinon elle rend le résultat à confier à Settle.
// Un tour qui n'a rien à publier est rendu tel quel.
func Follow(ctx context.Context, cfg Config, f forge.Forge, p Publisher, task harness.Task, res harness.Result) Followup {
	done := func(res harness.Result) Followup { return Followup{Task: task, Result: res} }
	if res.Outcome != harness.Succeeded {
		return done(res)
	}
	switch {
	case len(res.Patches) == 0:
		res.Outcome, res.Reason = harness.Failed, "l'agent annonce un succès sans aucun commit"
		return done(res)
	case p == nil:
		res.Outcome, res.Reason = harness.Failed, "aucune Publication n'est configurée"
		return done(res)
	}
	pub, err := p.Publish(ctx, task, res)
	if err != nil {
		res.Outcome, res.Reason = harness.Failed, "Publication échouée : "+err.Error()
		return done(res)
	}
	task.PullRequest = &pub.Draft

	checks, settled, err := waitCI(ctx, cfg, f, task.Ticket.Repo, pub.Head)
	failed := filter(checks, forge.CheckFailed)
	switch {
	case len(failed) == 0 && settled:
		res.Reason = fmt.Sprintf("CI verte au tour %d.\n\n%s", round(task), res.Reason)
		return done(res)
	case len(failed) == 0:
		why := fmt.Sprintf("CI non conclue au bout de %s", ciTimeout(cfg))
		if pending := filter(checks, forge.CheckPending); len(pending) > 0 {
			why += ", checks encore en cours : " + names(pending)
		} else if err != nil {
			why += " : " + err.Error()
		}
		res.Reason = why + " ; à vérifier sur la PR.\n\n" + res.Reason
		return done(res)
	case round(task) >= rounds(cfg):
		task.CIFailures = failed
		res.Outcome = CIFailed
		res.Reason = fmt.Sprintf("CI toujours rouge après %d tour(s) de l'agent.\n\n%s", round(task), Failures(failed))
		return done(res)
	}
	next := task
	next.Round = round(task) + 1
	next.BaseSHA = pub.Head
	next.CIFailures = failed
	next.PriorCostUSD += res.CostUSD
	next.PriorDuration += res.Duration
	return Followup{Retry: true, Task: next}
}

// waitCI lit les checks du commit sha jusqu'à ce qu'ils soient tous
// terminés, ou jusqu'à l'échéance. settled dit si tous l'étaient ; err est
// la dernière erreur de lecture, s'il n'y a eu aucune lecture réussie depuis.
func waitCI(ctx context.Context, cfg Config, f forge.Forge, repo, sha string) (checks []forge.Check, settled bool, err error) {
	wctx, cancel := context.WithTimeout(ctx, ciTimeout(cfg))
	defer cancel()
	tick := time.NewTicker(cmp.Or(cfg.CIPoll, DefaultCIPoll))
	defer tick.Stop()
	for {
		cs, cerr := f.Checks(wctx, repo, sha)
		if cerr == nil {
			checks, err = cs, nil
			if len(cs) > 0 && len(filter(cs, forge.CheckPending)) == 0 {
				return checks, true, nil
			}
		} else if wctx.Err() == nil {
			err = cerr
		}
		select {
		case <-wctx.Done():
			return checks, false, err
		case <-tick.C:
		}
	}
}

// Failures résume des checks en échec pour un humain, avec leur extrait.
func Failures(checks []forge.Check) string {
	var b strings.Builder
	b.WriteString("Checks en échec :\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "\n- **%s**", oneLine(c.Name))
		if c.URL != "" {
			fmt.Fprintf(&b, " (%s)", oneLine(c.URL))
		}
		if c.Excerpt != "" {
			b.WriteString("\n\n" + indent(c.Excerpt) + "\n")
		}
	}
	return b.String()
}

// indent met un extrait en bloc de code Markdown, quel que soit son contenu.
func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "      " + l
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func filter(checks []forge.Check, state forge.CheckState) []forge.Check {
	var out []forge.Check
	for _, c := range checks {
		if c.State == state {
			out = append(out, c)
		}
	}
	return out
}

func names(checks []forge.Check) string {
	var ns []string
	for _, c := range checks {
		ns = append(ns, oneLine(c.Name))
	}
	return strings.Join(ns, ", ")
}

// round rend le numéro du tour d'une tâche ; une tâche sans numéro est au
// premier.
func round(t harness.Task) int { return max(t.Round, 1) }

func rounds(cfg Config) int {
	if cfg.Rounds <= 0 {
		return MaxRounds
	}
	return min(cfg.Rounds, MaxRounds)
}

func ciTimeout(cfg Config) time.Duration { return cmp.Or(cfg.CITimeout, DefaultCITimeout) }

// Settle rend un ticket réservé dans un état explicite, avec le résultat de
// son dernier tour : aucun ticket ne reste en agent-in-progress, sauf si la
// forge refuse aussi le rendu, ce que l'erreur signale. Le coût et la durée
// rapportés cumulent tous les tours. Une CI rouge au dernier tour est aussi
// résumée en commentaire de la PR, qui reste en brouillon.
func Settle(ctx context.Context, cfg Config, f forge.Forge, task harness.Task, res harness.Result) (TicketReport, error) {
	t := task.Ticket
	res.CostUSD += task.PriorCostUSD
	res.Duration += task.PriorDuration
	tr := TicketReport{
		Repo: t.Repo, Number: t.Number, Title: t.Title,
		CostUSD: res.CostUSD, Duration: res.Duration, Transcript: res.Transcript,
	}
	draft := ""
	if task.PullRequest != nil {
		tr.PullRequest = task.PullRequest.URL
		draft = "\n\nPR en brouillon : " + task.PullRequest.URL
	}
	if r := round(task); r > 1 {
		draft += fmt.Sprintf(" (%d tours de l'agent)", r)
	}

	var label, comment string
	switch res.Outcome {
	case harness.Succeeded:
		// Machine d'états de la spec : en cas de succès, le ticket est relié
		// à sa PR en brouillon et ne porte plus de label de cycle de vie.
		comment = fmt.Sprintf("night-shift (passe %s) : travail terminé, PR en brouillon à relire : %s\n\n%s%s",
			cfg.ID, tr.PullRequest, res.Reason, accounting(res))
		if r := round(task); r > 1 {
			comment += fmt.Sprintf(" ; %d tours de l'agent", r)
		}
	case CIFailed:
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : la CI échoue encore, la PR reste en brouillon et le ticket revient à un humain.%s\n\n%s%s",
			cfg.ID, draft, res.Reason, accounting(res))
	case harness.NeedsInfo:
		label = forge.LabelNeedsInfo
		comment = fmt.Sprintf("night-shift (passe %s) : l'agent s'est arrêté, il lui manque une information.\n\nMotif : %s%s%s",
			cfg.ID, res.Reason, draft, accounting(res))
	case harness.Stopped:
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : arrêt motivé de l'agent, ticket rendu à un humain.\n\nMotif : %s%s%s",
			cfg.ID, res.Reason, draft, accounting(res))
	default:
		res.Outcome = harness.Failed
		label = forge.LabelHuman
		comment = fmt.Sprintf("night-shift (passe %s) : traitement interrompu, ticket rendu à un humain.\n\nCause : %s%s%s",
			cfg.ID, res.Reason, draft, accounting(res))
	}
	tr.Outcome, tr.Reason = res.Outcome, res.Reason
	comment += "\n\nRésultat provisoire : " + ProvisionalOutcome(res.Outcome)

	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	var errs []error
	if res.Outcome == CIFailed && task.PullRequest != nil {
		if err := f.Comment(cctx, t.Repo, task.PullRequest.Number, fmt.Sprintf(
			"night-shift (passe %s) : la CI échoue encore après %d tour(s) de l'agent. La PR reste en brouillon : à un humain de reprendre.\n\n%s",
			cfg.ID, round(task), Failures(task.CIFailures))); err != nil {
			errs = append(errs, fmt.Errorf("commentaire de la PR %s#%d : %w", t.Repo, task.PullRequest.Number, err))
		}
	}
	if err := release(cctx, f, t, label, comment); err != nil {
		errs = append(errs, fmt.Errorf("rendu de %s#%d, le ticket peut rester en %s : %w", t.Repo, t.Number, forge.LabelInProgress, err))
		return TicketReport{}, errors.Join(errs...)
	}
	return tr, errors.Join(errs...)
}

// ProvisionalOutcome dit, pour un humain, dans quel état la passe laisse un
// ticket selon l'issue de l'agent ; le Résultat définitif se lira sur la
// forge (PR mergée, fermée…).
func ProvisionalOutcome(o harness.Outcome) string {
	switch o {
	case harness.Succeeded:
		return "PR en brouillon à relire"
	case harness.NeedsInfo:
		return "rendu en " + forge.LabelNeedsInfo + ", il manque une information"
	case CIFailed:
		return "CI rouge, PR en brouillon rendue à un humain (" + forge.LabelHuman + ")"
	default:
		return "rendu à un humain (" + forge.LabelHuman + ")"
	}
}

// accounting résume le coût, la durée et la session de l'agent, quand il
// les a rapportés.
func accounting(res harness.Result) string {
	if res.Duration == 0 && res.CostUSD == 0 && res.SessionID == "" {
		return ""
	}
	return fmt.Sprintf("\n\nDurée : %s, coût : %.2f $, session : %s", res.Duration.Round(time.Second), res.CostUSD, res.SessionID)
}

func reserve(ctx context.Context, cfg Config, f forge.Forge, t forge.Ticket) error {
	if err := f.AddLabel(ctx, t.Repo, t.Number, forge.LabelInProgress); err != nil {
		return err
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelReady); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, fmt.Sprintf(
		"night-shift : ticket réservé par la passe %s le %s.", cfg.ID, cfg.Now().UTC().Format(time.RFC3339)))
}

// retriage sort un ticket de la file sans le traiter : il repasse en
// needs-triage, avec un commentaire qui explique pourquoi sans citer le
// contenu en cause.
func retriage(ctx context.Context, cfg Config, f forge.Forge, t forge.Ticket, reason string) error {
	if err := f.AddLabel(ctx, t.Repo, t.Number, forge.LabelTriage); err != nil {
		return err
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelReady); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, fmt.Sprintf(
		"night-shift (passe %s) : ticket non traité et sorti de la file : %s. "+
			"L'agent ne reçoit que le contenu d'auteurs de confiance ; à un humain de juger si le brief doit changer, puis de remettre le ticket en %s.",
		cfg.ID, reason, forge.LabelReady))
}

// rollback remet au mieux un ticket dans la file après une réservation
// partielle ; ses erreurs sont ignorées, celle de la réservation fait foi.
func rollback(ctx context.Context, f forge.Forge, t forge.Ticket) {
	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	_ = f.AddLabel(cctx, t.Repo, t.Number, forge.LabelReady)
	_ = f.RemoveLabel(cctx, t.Repo, t.Number, forge.LabelInProgress)
}

func release(ctx context.Context, f forge.Forge, t forge.Ticket, label, comment string) error {
	if label != "" {
		if err := f.AddLabel(ctx, t.Repo, t.Number, label); err != nil {
			return err
		}
	}
	if err := f.RemoveLabel(ctx, t.Repo, t.Number, forge.LabelInProgress); err != nil {
		return err
	}
	return f.Comment(ctx, t.Repo, t.Number, comment)
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// dedupe retire les dépôts en double, sans tenir compte de la casse comme
// les forges.
func dedupe(repos []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range repos {
		k := strings.ToLower(r)
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}
