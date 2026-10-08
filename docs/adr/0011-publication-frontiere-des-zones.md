# La Publication fait respecter les zones avant de pousser ; le check en CI devient optionnel

Remplace l'ADR 0002 sur un point : les zones interdites ne sont plus protégées d'abord par un check CI requis. Depuis l'ADR 0005, seule la Publication pousse sur une branche `agent/`. Elle vérifie donc la série de l'agent contre l'adhésion de la **branche de base** avant de pousser, et le contenu poussé est exactement celui qu'elle a vérifié. Une zone interdite touchée bloque le push : le ticket est rendu à un humain (`ready-for-human`) avec la raison, et la règle s'applique à chaque push, tours de CI compris. Une zone sensible touchée est poussée, puis signalée dans la demande de changement par une mention et le label `sensitive-zone`, et un humain décide.

Le check en CI (l'action GitHub `zone-check`) reste disponible comme défense en profondeur optionnelle. Il n'est plus une condition d'adhésion, et il n'a pas d'équivalent GitLab.

## Considered Options

- **Garder le check CI requis comme frontière.** Écarté : il fait reposer le garde-fou sur un branchement correct dans chaque dépôt cible, contraire au critère « zéro miette » (ADR 0007). Sur GitLab, un pipeline de merge request exécute la configuration de la branche source, que l'agent pourrait réécrire. Les parades (pipeline execution policy, compliance pipeline) exigent une licence Premium ou Ultimate, ou une configuration externe par projet.

## Consequences

- La condition d'adhésion « le dépôt peut imposer des checks requis » disparaît. Restent : une branche par défaut protégée, et une identité de service sans droit de merge (ADR 0003).
- La CI d'un dépôt cible exécute toujours le code de l'agent (tests, Makefile), comme pour toute contribution. `.github/**` ou `.gitlab-ci.yml` restent des zones interdites, et une branche `agent/` non protégée ne voit pas les variables protégées.
