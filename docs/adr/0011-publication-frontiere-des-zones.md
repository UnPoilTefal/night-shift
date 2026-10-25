# La Publication fait respecter les zones avant de pousser ; le check en CI devient optionnel

Remplace l'ADR 0002 sur un point : les zones interdites ne sont plus protégées d'abord par un check CI requis. Depuis l'ADR 0005, seule la Publication pousse sur une branche `agent/`. Elle vérifie donc la série de l'agent contre l'adhésion de la **branche de base** avant de pousser, et le contenu poussé est exactement celui qu'elle a vérifié. Une zone interdite touchée bloque le push : le ticket est rendu à un humain (`ready-for-human`) avec la raison, et la règle s'applique à chaque push, tours de CI compris. Une zone sensible touchée est poussée, puis signalée dans la demande de changement par une mention et le label `sensitive-zone`, et un humain décide.

Le check en CI (l'action GitHub `zone-check`) reste disponible comme défense en profondeur optionnelle. Il n'est plus une condition d'adhésion, et il n'a pas d'équivalent GitLab.

## Considered Options

- **Garder le check CI requis comme frontière.** Écarté : il fait reposer le garde-fou sur un branchement correct dans chaque dépôt cible, contraire au critère « zéro miette » (ADR 0007). Sur GitLab, un pipeline de merge request exécute la configuration de la branche source, que l'agent pourrait réécrire. Les parades (pipeline execution policy, compliance pipeline) exigent une licence Premium ou Ultimate, ou une configuration externe par projet.

## Consequences

- La condition d'adhésion « le dépôt peut imposer des checks requis » disparaît. Restent : une branche par défaut protégée, et une identité de service sans droit de merge (ADR 0003).
- La CI d'un dépôt cible exécute toujours le code de l'agent (tests, Makefile), comme pour toute contribution. `.github/**` ou `.gitlab-ci.yml` restent des zones interdites, et une branche `agent/` non protégée ne voit pas les variables protégées.
- La Publication vérifie **chaque commit** de la série, et pas seulement le diff net : un commit qui touche une zone interdite puis un commit qui l'annule sont refusés, car chaque commit poussé reste dans l'historique et peut déclencher un workflow.
- L'adhésion vérifiée est celle du commit de base du **premier tour**, transporté dans la tâche, et jamais celle de la tête de la demande de changement. Absente ou invalide à ce commit, la Publication refuse de pousser.
- Les zones se jugent au **chemin** : un lien symbolique placé dans une zone interdite est refusé, et un lien placé hors zone qui pointe vers une zone ne l'est pas, puisqu'il ne modifie aucun fichier de la zone.
- Le refus de zone est une issue distincte d'une panne, dans le résumé, le digest et le commentaire du ticket.
- **Risque accepté, R1** : un workflow existant déclenché par une branche `agent/` ou par sa demande de changement exécute du code que l'agent peut modifier hors des zones interdites. L'adhésion exige donc qu'aucun de ces workflows n'ait de droit d'écriture ni de secret, et night-shift ne le vérifie pas : une vérification propre à chaque forge (permissions par défaut, workflows réutilisables, secrets d'organisation) n'a pas d'équivalent commun. Elle pourra se loger plus tard dans l'adaptateur de la forge de code, derrière le contrat que sépare #57, si le besoin s'exprime.
- **Risque accepté, R2** : l'étape agent refuse un jeton de forge reconnu par le nom de sa variable ou par la forme de sa valeur (`ghp_`, `github_pat_`, `gho_`, `ghs_`, `ghu_`, `glpat-`). Un jeton monté dans un fichier n'est pas détecté. Cette détection reste un garde-fou : la garantie repose sur le déploiement, qui ne monte aucun jeton chez l'agent.
- Au tour de CI, la demande de changement existe déjà. Un refus de zone la laisse ouverte en brouillon, avec un commentaire qui donne le chemin et la zone, et rend le ticket. Une zone sensible touchée y ajoute le label `sensitive-zone`, créé s'il manque, et un commentaire qui nomme les chemins.
