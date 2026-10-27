package cli

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/optin"
	"github.com/UnPoilTefal/night-shift/internal/weekly"
)

// runWeekly publie l'issue hebdomadaire de chaque dépôt candidat : étape de
// confiance, sans modèle ni état local, qui relit tout dans la forge. Un
// dépôt sans adhésion valide n'en reçoit pas. Code de sortie : 0 si chaque
// dépôt a été traité, 1 si l'un a échoué, 2 pour un usage incorrect.
func runWeekly(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("weekly", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var repos repoList
	fs.Var(&repos, "repo", repoUsage())
	week := fs.String("week", "", "un jour (AAAA-MM-JJ) de la semaine ISO à rapporter, en UTC ; par défaut, la semaine en cours")
	ff := addForgeFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	now := time.Now().UTC()
	day := now
	if *week != "" {
		d, err := time.Parse(time.DateOnly, *week)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "weekly : --week attend une date AAAA-MM-JJ")
			return 2
		}
		day = d
	}
	if len(repos) == 0 {
		_, _ = fmt.Fprintln(stderr, "weekly : au moins un --repo est requis")
		return 2
	}
	f, _, ok := ff.open("weekly", stderr, repos...)
	if !ok {
		return 2
	}

	ctx, stop := signalContext()
	defer stop()
	from, to := weekly.Week(day)
	code := 0
	for _, repo := range repos {
		data, found, err := f.File(ctx, repo, optin.Path)
		switch {
		case err != nil:
			_, _ = fmt.Fprintf(stderr, "weekly %s : %v\n", repo, err)
			code = 1
			continue
		case !found:
			_, _ = fmt.Fprintf(stdout, "weekly %s : aucune adhésion, pas d'issue\n", repo)
			continue
		}
		if _, err := optin.Parse(data); err != nil {
			_, _ = fmt.Fprintf(stdout, "weekly %s : adhésion invalide, pas d'issue\n", repo)
			continue
		}
		r, err := weekly.Derive(ctx, f, f, repo, from, to)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "weekly %s : %v\n", repo, err)
			code = 1
			continue
		}
		url, err := weekly.Publish(ctx, f, r, now)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "weekly %s : %v\n", repo, err)
			code = 1
			continue
		}
		trust := ""
		if r.TrustReached() {
			trust = ", critère de palier atteint"
		}
		_, _ = fmt.Fprintf(stdout, "weekly %s : %s (%d ticket(s), %d/%d mergés, %d needs-info%s)\n",
			repo, url, len(r.Tickets), r.Merged, r.Denominator, r.NeedsInfo, trust)
	}
	return code
}
