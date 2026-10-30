# night-shift

Vocabulaire de night-shift : des passes planifiées d'agents IA qui traitent les tickets prêts d'un issue tracker. Ce fichier est un glossaire : les décisions vivent dans `docs/adr/`.

Ce fichier s'appelait `CONTEXT.md` jusqu'au 2026-10-08 : les issues plus anciennes le citent sous ce nom.

Chaque terme a un **nom canonique anglais**, employé dans le code, les ressources Kubernetes et la documentation anglaise, et un **alias français** (`_FR_`), employé dans la prose française (ADR, specs, tickets). Les deux désignent la même chose. Voir l'ADR 0006.

## Language

### File de travail

**Ready ticket**:
Une issue qui porte le rôle `ready-for-agent`, avec un brief d'agent qui fait foi. Poser ce rôle, c'est déléguer : un humain le pose, ou un producteur déterministe appliquant une règle d'équipe sans équivoque, jamais un modèle qui a lu du contenu tiers. night-shift prend tout ticket prêt, quelle que soit son origine.
_FR_: ticket prêt
_Avoid_: tâche, job, task

**Pass**:
Une exécution, lancée par un poste, qui prend un instantané de la file des tickets prêts, en traite au plus un nombre plafonné, puis se clôt par un digest.
_FR_: passe
_Avoid_: run, batch, cycle

**Shift**:
La déclaration, faite en libre-service par une équipe, qui demande à night-shift de lancer des passes selon un déclencheur, sur une seule source de tickets, avec un profil d'exécution donné. L'adhésion vient du repo, qui consent ; le poste vient de l'équipe, qui commande.
_FR_: poste
_Avoid_: agent, manifeste, job, workflow

**Tenant**:
Le périmètre d'une équipe sur la plateforme : son namespace, ses secrets, ses postes et son quota. La plateforme fournit l'opérateur, les profils d'exécution et les garde-fous, qu'un tenant ne peut pas desserrer. Un homelab est une plateforme à un seul tenant.
_FR_: tenant
_Avoid_: client, locataire, projet, équipe

**Trigger**:
Ce qui demande à un poste de lancer une passe : une échéance cron, une demande manuelle, plus tard un événement de la forge ou du tracker. Un déclencheur demande toujours une passe, jamais le traitement d'un ticket isolé ; une demande reçue pendant qu'une passe est active se fond dans une seule passe en attente.
_FR_: déclencheur
_Avoid_: schedule, hook, webhook

**Mission**:
La nature du travail qu'un poste confie à ses passes, désignée par un slug, qui fixe à la fois la source du travail et sa configuration propre. Un poste a exactement une mission : d'abord le traitement des tickets prêts, plus tard la résolution d'incident sur alerte.
_FR_: mission
_Avoid_: tâche, type de tâche, job, task type

**Ticket source**:
L'endroit où vivent les tickets prêts d'un poste, quel que soit l'outil qui les porte : un repo GitHub, un projet GitLab, un projet Jira, ou tout outil qui a son adaptateur. C'est le périmètre sur lequel une seule passe est active à la fois.
_FR_: source de tickets
_Avoid_: backlog, tracker, repo

**Target repository**:
Le dépôt de code où une demande de changement d'agent est ouverte et où vit l'adhésion. Il se confond avec la source de tickets quand la forge de code sert aussi de tracker (GitHub, GitLab) ; avec un tracker séparé (Jira), le brief le désigne parmi ceux qu'autorise le poste.
_FR_: dépôt cible
_Avoid_: repo de destination, target

**Reservation**:
Le marquage qui réserve un ticket prêt à une passe, pour qu'aucun autre agent ni humain ne le prenne.
_FR_: réservation
_Avoid_: lock, claim

### Outils

**Code forge**:
L'outil qui héberge le dépôt cible : le code, ses branches, ses demandes de changement et leurs checks. GitHub et GitLab en sont deux ; night-shift n'en suppose aucune.
_FR_: forge de code
_Avoid_: GitHub, GitLab (noms d'un outil), SCM

**Harness**:
L'outil qui fait travailler un agent sur un clone à partir d'un brief, et rend sa série de commits et un résultat structuré. Claude Code en est un.
_FR_: harnais
_Avoid_: agent (le modèle au travail), runner, wrapper

**Adapter**:
Le branchement d'un outil concret sur un contrat de night-shift : une source de tickets, une forge de code, un harnais ou une notification. Ajouter un outil, c'est ajouter un adaptateur, jamais toucher la passe.
_FR_: adaptateur
_Avoid_: connecteur, plugin, intégration, driver

**Contract test suite**:
Le jeu de scénarios commun qu'un adaptateur doit passer pour être accepté, joué aussi contre le faux en mémoire, qui reste ainsi fidèle aux vrais outils.
_FR_: suite de contrat
_Avoid_: tests d'intégration, tests de conformité

### Exécution

**Execution profile**:
Ce qui tourne à la place de l'agent pour un ticket (image, commande, harnais, outils), fourni par la plateforme ou défini par l'équipe. Il choisit ce qui tourne, jamais ce qui lui est permis.
_FR_: profil d'exécution
_Avoid_: runtime, template, exécuteur

**Agent configuration**:
L'ensemble versionné de ce qui garde l'agent et de ce qu'il lit (refus ajoutés, skills), distinct de l'image : l'image porte les outils, la configuration est faite de données. Une étape de confiance la lit dans son propre dépôt, à la dernière révision fusionnée, et la remet à l'agent en lecture seule ; la révision est enregistrée pour chaque ticket. Elle ne peut qu'ajouter des refus aux refus fixes du harness, jamais en retirer, et son dépôt est une zone interdite à toutes les passes. Un enseignement promu n'oblige jamais à reconstruire une image.
_FR_: configuration de l'agent
_Avoid_: profil (c'est le profil d'exécution), settings, réglages

**Publication**:
L'étape de confiance, sans modèle, qui transforme le patch produit par l'agent en branche `agent/` et en demande de changement. Elle seule pousse du code sur la forge.
_FR_: publication
_Avoid_: push, livraison

**Round**:
Un passage de l'agent sur un ticket réservé, suivi de la publication de sa série et de l'attente de la CI de la demande de changement. Une passe en fait au plus deux par ticket : le second, la relance, n'a lieu que si la CI du premier échoue, et repart de la branche publiée.
_FR_: tour
_Avoid_: itération, essai, attempt

**Relay**:
Le mode où chaque étape d'une passe tourne en service permanent et attend son tour, au lieu d'être lancée dans l'ordre par un orchestrateur. Les étapes de confiance l'attendent auprès du service de contrôle ; l'agent, dans les volumes de la passe, par un marqueur de tour qui porte l'identifiant de la passe et une échéance. C'est ainsi que les étapes tournent sous Docker Compose.
_FR_: relais
_Avoid_: pipeline, chaîne, workflow

**Control service**:
Le service permanent (`night-shift control`) qui déclenche les passes hors Kubernetes, à l'échéance ou à la demande, n'en laisse tourner qu'une à la fois, sert le relais des étapes de confiance et tient l'historique des passes. Il ne détient aucun jeton de forge ni de modèle. C'est l'adaptateur Compose du plan de contrôle.
_FR_: service de contrôle
_Avoid_: scheduler, ordonnanceur, daemon, orchestrateur

**Change request**:
La proposition de modification qu'ouvre la publication sur le dépôt cible : une pull request sur GitHub, une merge request sur GitLab. Elle est en brouillon, sauf si le palier de confiance du repo autorise une demande prête. night-shift ne la merge jamais et ne l'approuve jamais.
_FR_: demande de changement
_Avoid_: PR, MR (noms propres à une forge), patch

**Service identity**:
L'identité sous laquelle night-shift agit sur la forge (réservation, publication) et auprès du modèle, distincte de celle d'un humain qui le pilote ou dérivée d'elle.
_FR_: identité de service
_Avoid_: bot, compte technique

### Frontière

**Verdict**:
La décision qu'une réalisation rend sur une action de l'agent : refusée, renvoyée à la porte, refusée à expiration, signalée, laissée, ou arrêtée par un plafond. Il est écrit dans le journal des verdicts par l'étape ou la brique qui l'a observé, jamais par l'agent, avec la classe et l'empreinte de l'action mais jamais son verbatim. La forge n'en reçoit que les compteurs. Une décision humaine n'est jamais portée par le journal : elle vit sur le ticket ou dans la configuration de l'agent (keystone#78).
_FR_: verdict
_Avoid_: trace, log, événement

**Proof**:
Un jeu de cas où l'on ordonne à l'agent une action dangereuse (pousser sur la branche par défaut, lire un secret…), puis où l'on constate par l'état, jamais par le récit de l'agent, si une barrière l'a arrêtée. Chaque cas est une fiche déclarative : préparation, consigne, attendu par palier, garantie visée, vérifications. L'épreuve mesure les barrières, là où l'éval de régression mesure la qualité du travail, et ses fiches en sont la graine.
_FR_: épreuve
_Avoid_: test d'intrusion, pentest, benchmark

**Proof environment**:
L'environnement propre à night-shift où se joue l'épreuve : créé et détruit par l'outillage du dépôt, sur ses propres dépôts jetables et ses propres secrets. Il peut occuper un hôte prêté, sans rien partager avec un déploiement d'usage.
_FR_: environnement d'épreuve
_Avoid_: stack de test, banc d'essai, terrain

**Usage deployment**:
Un déploiement de night-shift au service d'un projet qui l'adopte. Il consomme le produit publié, comme n'importe quel utilisateur, et ne sert jamais à éprouver ou développer night-shift.
_FR_: déploiement d'usage
_Avoid_: prod, stack utilisateur

**Forbidden zone**:
Un chemin qu'une demande de changement d'agent ne doit jamais modifier, garanti par un check déterministe et non par la seule consigne donnée à l'agent.
_FR_: zone interdite
_Avoid_: blacklist, fichier protégé

**Sensitive zone**:
Un chemin qu'une demande de changement d'agent peut modifier, mais dont toute modification est signalée par un check déterministe pour décision humaine.
_FR_: zone sensible
_Avoid_: zone surveillée, zone grise

**Zone refusal**:
Le refus, par la Publication, de pousser une série de l'agent dont un commit touche une zone interdite ; le ticket est rendu à un humain, et le refus se compte à part d'une panne.
_FR_: refus de zone
_Avoid_: blocage, rejet, échec de Publication

**Opt-in**:
La déclaration, versionnée dans le repo lui-même, par laquelle l'équipe propriétaire le rend éligible aux passes et en fixe le périmètre.
_FR_: adhésion
_Avoid_: enrôlement, inscription, enrollment

### Relecture et confiance

**Digest**:
Le compte rendu d'une passe, destiné à la relecture humaine.
_FR_: digest
_Avoid_: rapport, log, report

**Outcome**:
L'issue d'un ticket prêt après une passe, dérivée de la forge : mergé tel quel, mergé après retouches, fermé sans merge, ou rendu à un humain.
_FR_: résultat
_Avoid_: statut, score, result

**Delegation rate**:
Sur une période et pour une source de tickets, la part des tickets réservés dont la demande de changement a été mergée par un humain, telle quelle ou après retouches. Les tickets rendus en `needs-info` sortent du calcul et sont comptés à part : ils mesurent l'écriture du brief, pas la délégation.
_FR_: taux de délégation
_Avoid_: taux de succès, success rate, taux d'automatisation

**Trusted author**:
Un auteur dont le contenu peut entrer dans le brief transmis à l'agent : associé au dépôt par la forge (propriétaire, membre, collaborateur), membre du rôle de projet Jira que nomme le poste quand la source de tickets est Jira, ou nommé par l'adhésion. Tout autre auteur est un tiers.
_FR_: auteur de confiance
_Avoid_: mainteneur, auteur autorisé

**Trust level**:
Un niveau d'autonomie accordé aux passes d'un repo (tickets par passe, demande de changement en brouillon ou prête), franchi sur décision humaine au vu des résultats.
_FR_: palier de confiance
_Avoid_: niveau d'autonomie, mode, autonomy level

**Pilot**:
Le premier repo ouvert aux passes, choisi parce qu'il porte déjà tous les garde-fous déterministes.
_FR_: pilote
_Avoid_: repo de test, sandbox
