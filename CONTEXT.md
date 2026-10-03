# night-shift

Vocabulaire de night-shift : des passes planifiées d’agents IA qui traitent les tickets prêts d’un issue tracker. Ce fichier est un glossaire : les décisions vivent dans `docs/adr/`.

## Language

**Ticket prêt**:
Une issue qui porte le rôle `ready-for-agent`, avec un brief d'agent qui fait foi.
_Avoid_: tâche, job

**Passe**:
Une exécution, lancée par un Poste, qui prend un instantané de la file des tickets prêts, en traite au plus un nombre plafonné, puis se clôt par un Digest.
_Avoid_: run, batch, cycle

**Poste**:
La déclaration, faite en libre-service par une équipe, qui demande à night-shift de lancer des passes selon un déclencheur, sur une seule source de tickets, avec un Profil d'exécution donné. L'Adhésion vient du repo, qui consent ; le Poste vient de l'équipe, qui commande.
_Avoid_: agent, manifeste, job, workflow

**Mission**:
La nature du travail qu'un Poste confie à ses passes, désignée par un slug, qui fixe à la fois la source du travail et sa configuration propre. Un Poste a exactement une Mission : d'abord le traitement des tickets prêts, plus tard la résolution d'incident sur alerte.
_Avoid_: tâche, type de tâche, job

**Source de tickets**:
L'endroit où vivent les tickets prêts d'un Poste : un repo GitHub, un projet GitLab ou un projet Jira. C'est le périmètre sur lequel une seule passe est active à la fois.
_Avoid_: backlog, tracker, repo

**Dépôt cible**:
Le dépôt de code où une PR d'agent est ouverte et où vit l'Adhésion. Il se confond avec la source de tickets sur GitHub et GitLab ; avec Jira, le brief le désigne parmi ceux qu'autorise le Poste.
_Avoid_: repo de destination, target

**Profil d'exécution**:
Ce qui tourne à la place de l'agent pour un ticket (image, commande, harnais, outils), fourni par la plateforme ou défini par l'équipe. Il choisit ce qui tourne, jamais ce qui lui est permis.
_Avoid_: runtime, template, exécuteur

**Publication**:
L'étape de confiance, sans modèle, qui transforme le patch produit par l'agent en branche `agent/` et en PR brouillon. Elle seule pousse du code sur la forge.
_Avoid_: push, livraison

**Réservation**:
Le marquage qui réserve un ticket prêt à une passe, pour qu'aucun autre agent ni humain ne le prenne.
_Avoid_: lock, claim

**Zone interdite**:
Un chemin qu'une PR d'agent ne doit jamais modifier, garanti par un check déterministe et non par la seule consigne donnée à l'agent.
_Avoid_: blacklist, fichier protégé

**Zone sensible**:
Un chemin qu'une PR d'agent peut modifier, mais dont toute modification est signalée par un check déterministe pour décision humaine.
_Avoid_: zone surveillée, zone grise

**Adhésion**:
La déclaration, versionnée dans le repo lui-même, par laquelle l'équipe propriétaire le rend éligible aux passes et en fixe le périmètre.
_Avoid_: opt-in, enrôlement, inscription

**Digest**:
Le compte rendu d'une passe, destiné à la relecture humaine.
_Avoid_: rapport, log

**Identité de service**:
L'identité sous laquelle night-shift agit sur la forge (réservation, publication) et auprès du modèle, distincte de celle d'un humain qui le pilote ou dérivée d'elle.
_Avoid_: bot, compte technique

**Pilote**:
Le premier repo ouvert aux passes, choisi parce qu'il porte déjà tous les garde-fous déterministes.
_Avoid_: repo de test, sandbox

**Résultat**:
L'issue d'un ticket prêt après une passe, dérivée de la forge : mergé tel quel, mergé après retouches, fermé sans merge, ou rendu à un humain.
_Avoid_: statut, score

**Palier de confiance**:
Un niveau d'autonomie accordé aux passes d'un repo (tickets par passe, PR en brouillon ou prête), franchi sur décision humaine au vu des résultats.
_Avoid_: niveau d'autonomie, mode
