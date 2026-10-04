package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/digest"
	"github.com/UnPoilTefal/night-shift/internal/digest/discord"
	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/stage"
)

// WebhookEnv est la variable d'environnement, facultative, qui porte l'URL
// du webhook Discord du digest.
const WebhookEnv = "NIGHT_SHIFT_DISCORD_WEBHOOK"

// digestCode publie le digest de la passe et rend le code de sortie : 1 si
// une issue de digest n'a pas pu être ouverte. Une notification absente ou
// en échec ne fait pas échouer la passe ; le digest le signale.
func digestCode(ctx context.Context, f forge.Forge, r pass.Report, stdout, stderr io.Writer, cmd string) int {
	var n digest.Notifier
	if u := os.Getenv(WebhookEnv); u != "" {
		n = discord.New(u)
	}
	d, err := digest.Publish(ctx, f, n, r)
	for _, i := range d.Issues {
		_, _ = fmt.Fprintf(stdout, "digest %s : %s\n", i.Repo, i.URL)
	}
	switch {
	case n == nil:
		_, _ = fmt.Fprintf(stdout, "notification Discord non configurée (%s absente)\n", WebhookEnv)
	case d.NotifyErr != nil:
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, d.NotifyErr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s : %v\n", cmd, err)
		return 1
	}
	return 0
}

// publishDigest complète le rapport de la sélection avec le ticket rendu par
// la Publication, s'il y en a un, et publie le digest. Un rapport absent ou
// illisible n'empêche pas le rendu du ticket, déjà fait : il prive seulement
// la passe de digest. Code de sortie : 1 si la passe a connu un incident.
func publishDigest(ctx context.Context, f forge.Forge, d *stage.Dirs, tr *pass.TicketReport, stdout, stderr io.Writer) int {
	report, ok, err := d.ReadReport()
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "publish : %v ; pas de digest\n", err)
		return 1
	case !ok:
		_, _ = fmt.Fprintln(stderr, "publish : aucun rapport de la sélection, pas de digest")
		return 0
	}
	if tr != nil {
		report.Tickets = append(report.Tickets, *tr)
	}
	report.Finished = time.Now()
	code := digestCode(ctx, f, report, stdout, stderr, "publish")
	if len(report.Errors) > 0 {
		_, _ = fmt.Fprintf(stderr, "publish : la passe a connu %d incident(s), voir le digest\n", len(report.Errors))
		return 1
	}
	return code
}
