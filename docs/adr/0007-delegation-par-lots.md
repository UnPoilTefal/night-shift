# Délégation par lots : l'humain valide en amont et au merge, jamais pendant l'exécution

night-shift délègue une pile de tickets prêts et rend une pile de demandes de changement. L'humain intervient à deux moments : en amont, quand il découpe le travail et pose `ready-for-agent` (le brief d'un ticket prêt est le plan approuvé), et à la fin, quand il merge. Il n'intervient pas pendant une passe : pas de porte d'approbation de plan, pas de pilotage en direct, pas de conversation avec l'agent. Le produit se juge au **taux de délégation**, la part des tickets réservés dont la demande de changement est mergée par un humain.

Critère de conception associé, dit « zéro miette » : un dépôt cible n'a besoin que de son fichier d'adhésion ; un tenant, d'un poste et de ses secrets ; la plateforme, d'une seule chart Helm (opérateur, proxy de sortie mutualisé, profils prédéfinis). Toute feature qui ajoute une configuration dans un dépôt cible doit le justifier.

## Considered Options

- **Une usine interactive** (porte de plan avant toute écriture, transcripts en direct, pilotage en cours de route, chat), sur le modèle de `vtmocanu/uzi`. Écartée : la porte de plan fait doublon avec le brief, elle ramène l'humain dans la boucle d'exécution, et elle suppose une application avec état (base, interface, workers permanents) que l'équipe plateforme devrait opérer.
- **Du travail auto-généré** (automates qui inventent bugs, refactors ou features à traiter). Écarté : c'est du travail que personne n'a décrit ni validé. Les workflows d'équipe qui alimentent la file restent possibles hors de night-shift, sous le contrat de l'ADR 0009.
- **Le merge automatique par palier de confiance.** Écarté de night-shift : le merge humain est la mesure du taux de délégation. Une équipe qui le veut l'active par le mécanisme natif de sa forge, sous sa propre règle et sa propre identité ; l'identité de service ne reçoit jamais le droit de merger (ADR 0003).

## Consequences

- La qualité du brief conditionne tout : l'écriture des tickets prêts (spec, découpage) fait partie du parcours documenté, pas d'un prérequis implicite.
- La visibilité reste en lecture seule : coût et durée par ticket et par passe, digest, état des passes. Aucune vue ne permet d'agir sur une passe en cours.
- Les features reprises d'une usine interactive doivent s'insérer dans une passe : la reprise d'une demande de changement commentée en revue est traitée par une passe suivante, pas en direct.
