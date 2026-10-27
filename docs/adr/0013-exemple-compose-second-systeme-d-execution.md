# Docker Compose devient un second système d'exécution des mêmes étapes, sans logique propre

Amende l'ADR 0010 sur un point : elle écartait « une pile Docker Compose ». Un exemple `examples/compose/` est admis pour le palier solo. Il fait tourner sur un hôte Docker unique les mêmes étapes du même binaire que le `CronJob` (`select`, `agent`, `publish`, puis `retry-agent` et `retry-publish`), comme des services permanents d'une stack Compose. Docker Compose n'est que leur **second système d'exécution**. La logique de passe, la Publication, les tours de CI et le digest restent ceux du binaire (ADR 0004).

L'orchestration ne repose que sur des capacités **génériques** du binaire, utiles aussi hors Compose :

- les secrets lus dans des fichiers (`…_FILE`, #107) ;
- l'archivage des transcripts par les étapes de confiance (#104) ;
- le **relais** des étapes, où chaque étape attend son tour au lieu d'être lancée par un orchestrateur (#108) ;
- le **service de contrôle** `night-shift control`, qui déclenche les passes à l'échéance ou à la demande et tient leur historique (#109). Il est l'adaptateur Compose du plan de contrôle, dont le contrat sera fixé par la carte #103.

L'exemple n'a ni script ni logique propres : un fichier `compose.yaml`, la configuration figée du proxy de sortie et ses listes d'autorisation. Le démon Docker (`restart: unless-stopped`) le supervise. Il n'y a ni socket Docker, ni systemd, ni cron d'hôte.

## Considered Options

- **Garder le seul Kubernetes** (ADR 0010). Écarté : une partie des utilisateurs visés (un développeur seul, un NAS, un VPS) n'a pas de cluster, et n'en monterait un que pour night-shift.
- **Une pile Compose avec sa propre logique** (scripts d'enchaînement, conteneur qui lance les autres par le socket Docker). Écarté : ce sont les deux chemins à maintenir que refusait l'ADR 0010, et le socket Docker donnerait à un conteneur la main sur tout l'hôte.
- **Docker Compose comme orchestration des mêmes étapes**, retenu. L'objection de l'ADR 0010 visait une seconde logique, pas un second ordonnanceur des mêmes étapes.

## Réponses aux objections de l'ADR 0010

- **Deux chemins à maintenir.** La logique reste unique (ADR 0004). Le coût propre à Compose se limite à `control`, au relais, à l'exemple et à ses tests, statique et de bout en bout (#110, #111).
- **Le homelab cesserait d'être le pilote de l'offre plateforme.** Il reste sur l'opérateur, pilote de l'offre plateforme, et peut éprouver la stack Compose à côté.
- **Un proxy que l'équipe surveillée configure n'est plus un garde-fou.** Sous Compose, l'utilisateur *est* la plateforme : il n'y a pas de tenant à protéger de lui-même. Le proxy embarqué a une configuration figée, qui ne s'adapte que par ses listes d'autorisation, et un test statique refuse toute configuration qui affaiblit une garantie (ordre des règles, au plus une exception privée, listes sans domaine de forge ni domaine de premier niveau entier).

## Consequences

- **Interface publique sous semver.** `select`, `agent`, `publish` et `control` deviennent une interface publique, versionnée sous semver : leurs options, leurs variables (formes `…_FILE` comprises) et le format des volumes `state`, `work` et `out/`, marqueurs du relais compris. Une rupture exige une version majeure.
- **Le `CronJob` garde son sort.** Il reste l'exemple Kubernetes du palier solo jusqu'à la parité de l'opérateur, puis il est retiré (ADR 0010). L'exemple Compose ne le remplace pas.
- **Les mêmes garanties, et des écarts constatés.** L'exemple tient les garanties du `CronJob` : agent sans jeton de forge, sortie limitée au proxy, étapes séparées par une frontière de conteneur, transcripts hors de portée de l'agent. Un écart se constate et se teste au lieu de s'assouplir. Ceux qui restent sont les suivants :

  | Écart par rapport au `CronJob` | Constaté par |
  |---|---|
  | Le conteneur agent dure d'une passe à l'autre. Après chaque tour, le binaire, en PID 1, tue tous les autres processus et vide ses `tmpfs` (`HOME`, `CLAUDE_CONFIG_DIR`). | Scénario 6 du test de bout en bout (#111) : un faux agent hostile laisse un processus et des fichiers, rien ne subsiste à la passe suivante. |
  | La fin de l'agent est un marqueur qu'il peut forger. `publish` copie la sortie, puis ne valide et ne pousse que sa copie. | Scénario 7 (#111) : un faux agent annonce sa fin puis réécrit sa sortie. |
  | Le point d'entrée de l'image du proxy démarre en root, avec `SETUID` et `SETGID` seulement. | Test statique (#110), puis recette humaine. |
  | Docker Desktop ne garantit ni le mode `isolated` du réseau des agents, ni le `mode` des fichiers de secrets. | Documentation (`docs/compose.md`) : Docker Desktop sert à l'essai, pas au déploiement. |
  | La lisibilité des secrets par l'UID 65532 dépend de l'hôte et se constate sur Linux. | Scénario 5 (#111). |

- **Ce qui n'est pas dans cette décision.** Le webhook de forge et tout déclenchement par événement, avec l'examen d'exposition qu'exige l'ADR 0008, le contrat du plan de contrôle et la configuration poussée relèvent de la carte #103. Podman et Docker sans root sont hors périmètre.
- La feuille de route du README porte une ligne non cochée pour l'exemple. Rien d'autre ne le promet avant sa livraison (#110).

Statut : accepté ; livraison par les tickets de la spec #105 (#104, #107 à #111).
