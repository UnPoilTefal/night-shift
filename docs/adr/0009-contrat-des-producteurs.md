# Contrat des producteurs : un brief n'est de confiance que si aucun modèle n'a lu de contenu tiers pour l'écrire

night-shift prend tout ticket prêt, quelle que soit son origine. Des workflows d'équipe, hors de night-shift, peuvent remplir la file : un scan d'hygiène de dépôt qui ouvre des tickets, un triage qui applique des règles d'équipe. Poser `ready-for-agent`, c'est déléguer, et seuls deux acteurs y ont droit : un humain, ou un **producteur déterministe** appliquant une règle d'équipe sans équivoque. Un producteur qui fait lire du contenu tiers à un modèle (un triage assisté par LLM, par exemple) ne peut proposer que `needs-triage` ou `ready-for-human`. Un humain promeut ensuite le ticket. Seule l'identité d'un producteur déterministe peut figurer parmi les auteurs de confiance d'une adhésion.

## Considered Options

- **Faire confiance à tout producteur déclaré par l'équipe.** Écarté. Un tiers ouvre un bug porteur d'une injection, un triage par LLM le juge conforme, réécrit le brief sous l'identité de son bot et pose `ready-for-agent`. Si ce bot est auteur de confiance, l'injection entre dans le brief avec le sceau de confiance et contourne le filtre d'auteurs (ADR 0002) par l'amont.

## Consequences

- Le contrat est documenté pour les équipes. night-shift ne peut pas vérifier comment un producteur a écrit son contenu : il vérifie l'auteur, et l'adhésion engage l'équipe sur ce qu'elle déclare de confiance.
- Un scan qui lit le dépôt lui-même, et non des contributions de tiers, est un producteur déterministe acceptable.
