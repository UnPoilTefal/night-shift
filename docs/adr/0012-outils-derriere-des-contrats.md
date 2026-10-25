# night-shift ne parle à un outil que par un contrat, et chaque outil a son adaptateur

night-shift n'implémente jamais directement un outil externe. Le cœur ne connaît que des **contrats** : la passe, la Publication, le digest et les tours de CI. Chaque outil concret est branché par un **adaptateur** qui implémente un contrat, et seul le point de composition choisit les adaptateurs d'une passe (la CLI aujourd'hui, l'opérateur demain). Quatre contrats couvrent les outils d'une passe :

- la **source de tickets** : lister les tickets prêts, poser et retirer un label, commenter, ouvrir l'issue de digest ;
- la **forge de code** : lire un fichier du dépôt cible, cloner et pousser, ouvrir une demande de changement, lire ses checks ;
- le **harnais** : faire travailler un agent sur un clone, à partir d'un brief ;
- la **notification** : annoncer la fin d'une passe.

GitHub, GitLab, Jira, Claude Code ou Discord ne sont que des adaptateurs. Un nouvel outil s'ajoute par un nouvel adaptateur, sans changement du cœur. Un adaptateur est accepté quand il passe la **suite de contrat** commune, qui tourne aussi contre le faux en mémoire.

Aucun détail d'un outil ne sort de son adaptateur. Cela couvre le vocabulaire (« pull request », « merge request » : le contrat dit « demande de changement »), le transport git (adresse de clone, identifiant attendu avec le jeton), l'identité des commits et le format des identifiants de projet.

## Considered Options

- **Coder chaque outil dans le cœur, avec des branchements par outil.** Écarté : chaque nouvelle forge ou chaque nouveau tracker toucherait la passe, la Publication et le digest, et les garde-fous (filtre d'auteurs, refus de zone, rendu des tickets) devraient être revérifiés outil par outil.
- **Un seul contrat de forge pour les tickets et le code.** Écarté (#57) : un tracker sans dépôt (Jira) devrait implémenter des méthodes de code qu'il n'a pas, et une forge de code devrait servir de tracker même quand l'équipe en utilise un autre.
- **Ne viser que les outils d'une organisation donnée.** Écarté : le produit vise des équipes plateforme aux outils différents. Le premier adaptateur ajouté (GitLab) est un cas d'usage, pas un cap.

## Consequences

- Un besoin nouveau, comme le résultat d'une demande de changement (#10), s'exprime d'abord comme une méthode d'un contrat, avec son implémentation dans le faux en mémoire et sa place dans la suite de contrat. Les adaptateurs viennent ensuite.
- Un outil qui n'offre pas une capacité du contrat la fournit autrement dans son adaptateur, ou la déclare absente avec un repli défini dans le contrat. Par exemple, sans liens de blocage natifs, une ligne `Blocked by: #n` dans le corps du ticket compte comme un blocage. Le cœur ne teste jamais l'identité de l'outil.
- Les vérifications propres à un outil (par exemple les permissions des workflows GitHub, risque R1 de l'ADR 0011) se logent dans son adaptateur, derrière une méthode neutre du contrat.
- Le choix d'un adaptateur se fait par configuration : une option des étapes en solo, un membre de l'union `source` du poste dans l'opérateur.
- Le code actuel respecte en partie cette règle. La passe, la Publication et le digest ne voient que l'interface de forge, le harnais et la notification. Mais un seul contrat de forge mêle les tickets et le code, il parle encore de « PR », et le transport git ainsi que l'identité des commits supposent GitHub. #57 corrige ces écarts.
