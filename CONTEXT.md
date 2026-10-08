# night-shift

Vocabulaire de night-shift : des passes planifiées d'agents IA qui traitent les tickets prêts d'un issue tracker. Ce fichier est un glossaire : les décisions vivent dans `docs/adr/`.

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
L'endroit où vivent les tickets prêts d'un poste : un repo GitHub, un projet GitLab ou un projet Jira. C'est le périmètre sur lequel une seule passe est active à la fois.
_FR_: source de tickets
_Avoid_: backlog, tracker, repo

**Target repository**:
Le dépôt de code où une demande de changement d'agent est ouverte et où vit l'adhésion. Il se confond avec la source de tickets sur GitHub et GitLab ; avec Jira, le brief le désigne parmi ceux qu'autorise le poste.
_FR_: dépôt cible
_Avoid_: repo de destination, target

**Reservation**:
Le marquage qui réserve un ticket prêt à une passe, pour qu'aucun autre agent ni humain ne le prenne.
_FR_: réservation
_Avoid_: lock, claim

### Exécution

**Execution profile**:
Ce qui tourne à la place de l'agent pour un ticket (image, commande, harnais, outils), fourni par la plateforme ou défini par l'équipe. Il choisit ce qui tourne, jamais ce qui lui est permis.
_FR_: profil d'exécution
_Avoid_: runtime, template, exécuteur

**Publication**:
L'étape de confiance, sans modèle, qui transforme le patch produit par l'agent en branche `agent/` et en demande de changement. Elle seule pousse du code sur la forge.
_FR_: publication
_Avoid_: push, livraison

**Round**:
Un passage de l'agent sur un ticket réservé, suivi de la publication de sa série et de l'attente de la CI de la demande de changement. Une passe en fait au plus deux par ticket : le second, la relance, n'a lieu que si la CI du premier échoue, et repart de la branche publiée.
_FR_: tour
_Avoid_: itération, essai, attempt

**Change request**:
La proposition de modification qu'ouvre la publication sur le dépôt cible : une pull request sur GitHub, une merge request sur GitLab. Elle est en brouillon, sauf si le palier de confiance du repo autorise une demande prête. night-shift ne la merge jamais et ne l'approuve jamais.
_FR_: demande de changement
_Avoid_: PR, MR (noms propres à une forge), patch

**Service identity**:
L'identité sous laquelle night-shift agit sur la forge (réservation, publication) et auprès du modèle, distincte de celle d'un humain qui le pilote ou dérivée d'elle.
_FR_: identité de service
_Avoid_: bot, compte technique

### Frontière

**Forbidden zone**:
Un chemin qu'une demande de changement d'agent ne doit jamais modifier, garanti par un check déterministe et non par la seule consigne donnée à l'agent.
_FR_: zone interdite
_Avoid_: blacklist, fichier protégé

**Sensitive zone**:
Un chemin qu'une demande de changement d'agent peut modifier, mais dont toute modification est signalée par un check déterministe pour décision humaine.
_FR_: zone sensible
_Avoid_: zone surveillée, zone grise

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
