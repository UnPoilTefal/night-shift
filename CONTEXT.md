# night-shift

Vocabulaire de night-shift : des passes planifiées d’agents IA qui traitent les tickets prêts d’un issue tracker. Ce fichier est un glossaire : les décisions vivent dans `docs/adr/`.

## Language

**Ticket prêt**:
Une issue qui porte le rôle `ready-for-agent`, avec un brief d'agent qui fait foi.
_Avoid_: tâche, job

**Passe**:
Une exécution planifiée qui parcourt la file des tickets prêts et en traite au plus un nombre plafonné.
_Avoid_: run, batch, cycle

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
L'identité sous laquelle l'agent agit sur la forge et auprès du modèle, distincte de celle d'un humain qui le pilote ou dérivée d'elle.
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
