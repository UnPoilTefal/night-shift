# Le développement de night-shift ne s'exécute jamais hors de son propre périmètre

Est **utilisateur** ce que le produit publié offre et documente pour un projet qui l'adopte ; tout le reste est **dev** et ne s'exécute jamais hors du périmètre de night-shift. Tout projet qui met en œuvre night-shift est utilisateur. Un déploiement d'usage consomme donc le produit publié et ne sert jamais de banc d'essai, car un rejeu y emprunterait ses secrets, couperait ses passes planifiées et reposerait sur un savoir écrit hors de ce dépôt.

L'épreuve des barrières est du dev : elle se joue dans un environnement d'épreuve créé et détruit par l'outillage du dépôt. Son **rapport**, joint à la GitHub Release, est ce qui revient au produit.

## Considered Options

- **Une épreuve rejouable par chaque utilisateur sur son déploiement.** Écartée : il lui faudrait une surcharge, des dépôts jetables et un outillage qui ne sont pas du produit. En contrepartie, une équipe voit ce que night-shift a prouvé sur son environnement, pas sur le sien ; si ce besoin apparaît, ce sera un nouveau livrable utilisateur, pas un assouplissement de cette règle.
- **Garder la commande `proof` dans la CLI publiée.** Écartée : le même binaire est dans l'image de l'agent, et les fiches embarquées y exposeraient la liste des attaques à l'agent éprouvé.

## Consequences

- L'outil de rejeu et la surcharge `proof.yaml` quittent la CLI publiée et `examples/`. Le README sépare ce qui est offert aux utilisateurs de ce qui relève du développement.
- Un hôte prêté peut porter à la fois un déploiement d'usage et l'environnement d'épreuve : le périmètre se juge aux dépôts, aux secrets et aux fichiers, pas à la machine.
- Un rejeu informe la décision d'ouvrir un palier sans bloquer la release : un agent réel n'est pas déterministe et coûte du quota.
