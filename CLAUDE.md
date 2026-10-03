# night-shift

Passes planifiées d'agents IA sur les tickets prêts d'un issue tracker. Cadre conçu pour être portable : solo, puis équipe, puis offre d'une équipe platform. Contenu strictement générique : aucune topologie d'infrastructure ni aucun contexte d'organisation particulier.

Vocabulaire : `CONTEXT.md`. Décisions : `docs/adr/`.

Nommage (ADR 0006) : le code, les ressources Kubernetes et la doc anglaise utilisent le nom canonique anglais d'un terme ; la prose française (ADR, specs, tickets) utilise son alias `_FR_`.

## Agent skills

### Issue tracker

Issues GitHub de `UnPoilTefal/night-shift`, via `gh`. See `docs/agents/issue-tracker.md`.

### Triage labels

Les cinq rôles canoniques sous leur nom par défaut. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context : `CONTEXT.md` et `docs/adr/` à la racine. See `docs/agents/domain.md`.
