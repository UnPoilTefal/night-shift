# L'identité de service porte un sous-ensemble des droits de l'équipe, jamais leur totalité

L'agent est traité comme un membre d'équipe, mais d'un genre particulier : il prend pour instructions le texte qu'il lit. Ses droits sont donc ceux de l'équipe **moins** merge, approbation, tags, zones interdites et secrets, et pas les droits de l'équipe tels quels.

- **Solo** : un jeton dérivé du propriétaire est acceptable s'il est restreint aux seuls repos adhérents, à expiration courte, et si l'attribution reste obligatoire (branches `agent/`, trailer de commit) pour que l'audit distingue l'agent de l'humain.
- **Équipe et plateforme** : un compte de service par équipe, aux droits restreints comme ci-dessus. Chaque forge fournit son adaptateur (GitHub App avec jetons d'une heure, jeton d'accès de projet ou de groupe GitLab…).

Le jeton intégré à la CI de la forge (`GITHUB_TOKEN`) est écarté : les PR qu'il ouvre ne déclenchent pas les checks requis.
