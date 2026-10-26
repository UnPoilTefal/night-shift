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

	"github.com/UnPoilTefal/night-shift/internal/harness"
	"github.com/UnPoilTefal/night-shift/internal/pass"
)

// repoList porte les dépôts candidats ; leur forme est celle qu'attend la
// forge choisie, et son adaptateur la vérifie (forgeFlags.open).
type repoList []string

func (r *repoList) String() string { return strings.Join(*r, ",") }

func (r *repoList) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*r = append(*r, s)
		}
	}
	return nil
}

// runPass exécute une passe sur la forge choisie avec le harnais factice. Code de
// sortie : 0 si la passe s'est déroulée (même sans ticket traité), 1 en cas
// d'erreur, 2 pour un usage incorrect.
func runPass(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pass", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var repos repoList
	fs.Var(&repos, "repo", repoUsage())
	maxTickets := fs.Int("max-tickets", 1, "nombre maximal de tickets traités par la passe")
	ff := addForgeFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(repos) == 0 || *maxTickets < 1 {
		_, _ = fmt.Fprintln(stderr, "pass : au moins un --repo et --max-tickets >= 1 sont requis")
		return 2
	}
	f, _, ok := ff.open("pass", stderr, repos...)
	if !ok {
		return 2
	}

	now := time.Now
	cfg := pass.Config{
		Repos:      repos,
		MaxTickets: *maxTickets,
		ID:         "pass-" + now().UTC().Format("20060102T150405Z"),
		Now:        now,
	}
	// Un arrêt (Ctrl-C, arrêt du conteneur) annule la passe ; les tickets en
	// cours sont tout de même rendus, avec un contexte de nettoyage propre.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := pass.Run(ctx, cfg, f, f, harness.Stub{}, nil)
	printReport(stdout, report)
	code := digestCode(ctx, f, report, stdout, stderr, "pass")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pass : %v\n", err)
		return 1
	}
	return code
}

func printReport(w io.Writer, r pass.Report) {
	printRepos(w, r)
	if len(r.Tickets) == 0 {
		_, _ = fmt.Fprintln(w, "aucun ticket prêt éligible")
	}
	printTickets(w, r.Tickets)
	printRetriaged(w, r.Retriaged)
}

func printRepos(w io.Writer, r pass.Report) {
	_, _ = fmt.Fprintf(w, "passe %s\n", r.ID)
	for _, rr := range r.Repos {
		if rr.Detail != "" {
			_, _ = fmt.Fprintf(w, "dépôt %s : %s (%s)\n", rr.Repo, rr.Status, rr.Detail)
		} else {
			_, _ = fmt.Fprintf(w, "dépôt %s : %s\n", rr.Repo, rr.Status)
		}
	}
}

func printTickets(w io.Writer, ts []pass.TicketReport) {
	for _, t := range ts {
		_, _ = fmt.Fprintf(w, "ticket %s#%d « %s » : %s (%s)\n", t.Repo, t.Number, t.Title, t.Outcome, t.Reason)
		if t.ChangeRequest != "" {
			_, _ = fmt.Fprintf(w, "PR en brouillon : %s\n", t.ChangeRequest)
		}
	}
}

func printRetriaged(w io.Writer, rs []pass.RetriageReport) {
	for _, r := range rs {
		_, _ = fmt.Fprintf(w, "ticket %s#%d retrié (needs-triage) : %s\n", r.Repo, r.Number, r.Reason)
	}
}
