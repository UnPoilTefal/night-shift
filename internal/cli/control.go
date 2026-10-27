package cli

import (
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/robfig/cron/v3"

	"github.com/UnPoilTefal/night-shift/internal/control"
)

// ControlTokenEnv est la variable qui porte le jeton de l'API
// d'administration du service de contrôle.
const ControlTokenEnv = "NIGHT_SHIFT_CONTROL_TOKEN"

const controlUsage = `usage : night-shift control <serve|trigger> [options]

  serve     service de contrôle : échéance, demandes manuelles, relais des étapes de confiance, historique
  trigger   demande une passe au service, depuis son conteneur (docker compose exec control night-shift control trigger)
`

// runControl aiguille les sous-commandes du service de contrôle.
func runControl(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, controlUsage)
		return 2
	}
	switch args[0] {
	case "serve":
		return runControlServe(args[1:], stdout, stderr)
	case "trigger":
		return runControlTrigger(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "control : sous-commande inconnue %q\n\n%s", args[0], controlUsage)
	return 2
}

// runControlServe fait tourner le service jusqu'à son arrêt. Il ne détient
// aucun jeton de forge ni de modèle et ne fait aucun appel sortant.
func runControlServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("control serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	schedule := fs.String("schedule", "", "échéance des passes, expression cron à cinq champs, dans le fuseau du conteneur (UTC par défaut ; préfixe CRON_TZ=… accepté) ; absente, seules les demandes manuelles en déclenchent")
	listen := fs.String("listen", ":8080", "adresse de l'API des étapes de confiance, sur le réseau control-net, sans authentification")
	adminListen := fs.String("admin-listen", "", "adresse de l'API d'administration, en lecture seule, protégée par "+ControlTokenEnv+" ; à ne publier que sur 127.0.0.1 de l'hôte")
	history := fs.String("history", "", "dossier de l'historique des passes")
	transcripts := fs.String("transcripts", "", "archive des transcripts, que le service purge seulement")
	retention := fs.Duration("retention", control.DefaultRetention, "durée de conservation de l'historique et des transcripts")
	watchdog := fs.Duration("watchdog", control.DefaultWatchdog, "durée maximale d'une passe : au-delà, elle est déclarée échouée ; doit dépasser la somme des délais des étapes")
	requests := fs.String("requests", control.DefaultRequests, "dossier des demandes manuelles, propre au conteneur du service")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *history == "" || *retention <= 0 || *watchdog <= 0 {
		_, _ = fmt.Fprintln(stderr, "control serve : --history est requis, --retention et --watchdog sont positives")
		return 2
	}
	cfg := control.Config{Watchdog: *watchdog, History: *history, Transcripts: *transcripts, Retention: *retention}
	if *schedule != "" {
		s, err := cron.ParseStandard(*schedule)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "control serve : --schedule invalide : %v\n", err)
			return 2
		}
		cfg.Schedule = s
	}
	token := ""
	if *adminListen != "" {
		var err error
		if token, err = secretEnv(ControlTokenEnv); err != nil || token == "" {
			_, _ = fmt.Fprintf(stderr, "control serve : --admin-listen exige le jeton %s ou %s%s\n", ControlTokenEnv, ControlTokenEnv+FileSuffix, errSuffix(err))
			return 2
		}
	}

	svc, err := control.New(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "control serve : %v\n", err)
		return 1
	}
	var l control.Listeners
	if l.Stages, err = net.Listen("tcp", *listen); err != nil {
		_, _ = fmt.Fprintf(stderr, "control serve : %v\n", err)
		return 1
	}
	if *adminListen != "" {
		if l.Admin, err = net.Listen("tcp", *adminListen); err != nil {
			_ = l.Stages.Close()
			_, _ = fmt.Fprintf(stderr, "control serve : %v\n", err)
			return 1
		}
	}
	_, _ = fmt.Fprintf(stdout, "control : étapes sur %s, administration sur %s\n", l.Stages.Addr(), adminAddr(l))
	ctx, stop := signalContext()
	defer stop()
	if err := svc.Serve(ctx, l, token, *requests, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "control serve : %v\n", err)
		return 1
	}
	return 0
}

func adminAddr(l control.Listeners) string {
	if l.Admin == nil {
		return "(désactivée)"
	}
	return l.Admin.Addr().String()
}

// runControlTrigger dépose une demande de passe, que le service consomme :
// sans réseau ni secret.
func runControlTrigger(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("control trigger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	requests := fs.String("requests", control.DefaultRequests, "dossier des demandes manuelles du service")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := control.Trigger(*requests); err != nil {
		_, _ = fmt.Fprintf(stderr, "control trigger : %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "passe demandée : elle rejoint la passe en attente s'il y en a une")
	return 0
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}
