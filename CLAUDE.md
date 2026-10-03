# night-shift

Passes planifiées d'agents IA sur les tickets prêts d'un issue tracker. Cadre conçu pour être portable : solo, puis équipe, puis offre d'une équipe platform. Contenu strictement générique : aucune topologie d'infrastructure ni aucun contexte d'organisation particulier.

Vocabulaire : `CONTEXT.md`. Décisions : `docs/adr/`.

Nommage (ADR 0006) : le code, les ressources Kubernetes et la doc anglaise utilisent le nom canonique anglais d'un terme ; la prose française (ADR, specs, tickets) utilise son alias `_FR_`.

README : à chaque ticket traité, mettre à jour `README.md` (en anglais) dans la même PR, pour qu'il reste exact et vendeur : cocher l'étape de la feuille de route, ajuster le statut et ses badges, documenter ce qui devient utilisable (commandes, exemples, schéma). Aucun badge cassé et aucune promesse non livrée.

## Développement

- `make help` liste les cibles ; la CI appelle les mêmes (`make test`, `make test-e2e`).
- Après toute modification de `api/` (types des CRD, marqueurs kubebuilder), lancer `make manifests generate` : la CI échoue si le code ou les manifestes générés ne sont pas à jour.
- Les clusters kind (`make kind-up`, `make test-e2e`…) utilisent `bin/kind.kubeconfig`. Ne jamais lancer `make install`, `make deploy` ou `kubectl` sans `KUBECONFIG` explicite : le contexte courant peut viser un vrai cluster.

## Agent skills

### Issue tracker

Issues GitHub de `UnPoilTefal/night-shift`, via `gh`. See `docs/agents/issue-tracker.md`.

### Triage labels

Les cinq rôles canoniques sous leur nom par défaut. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context : `CONTEXT.md` et `docs/adr/` à la racine. See `docs/agents/domain.md`.
