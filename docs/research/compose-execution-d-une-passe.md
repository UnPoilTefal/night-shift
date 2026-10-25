# Compose : exécution d'une passe à étapes

Constat de recherche pour le ticket #91 (carte #90). Question : comment Docker Compose exécute-t-il une passe à étapes ponctuelles, au plus près du pod du `CronJob` d'`examples/solo` (cinq conteneurs enchaînés, volumes partagés, deux tours au plus) ?

Ce document rapporte des faits. Il ne recommande aucune conception.

## Sources et méthode

- **Sources primaires**, lues le 2026-10-25 :
  - la spécification Compose, branche `main` de [compose-spec/compose-spec](https://github.com/compose-spec/compose-spec) ;
  - la référence des commandes de Compose, dans [docker/compose/docs/reference](https://github.com/docker/compose/tree/main/docs/reference), publiée sur [docs.docker.com/reference/cli/docker/compose](https://docs.docker.com/reference/cli/docker/compose/) ;
  - les issues, PR et notes de version de [docker/compose](https://github.com/docker/compose) ;
  - la documentation de Docker Engine ([docker/docs](https://github.com/docker/docs), [docker/cli](https://github.com/docker/cli/tree/master/docs/reference/commandline)).
- **Versions** : la dernière release de Compose est [v5.6.1](https://github.com/docker/compose/releases/tag/v5.6.1) (2026-10-21). La spécification de `main` contient des éléments qu'aucune release ne livre encore entièrement (voir « Jobs »).
- **Observations** : les points marqués *(observé)* viennent d'essais menés le 2026-10-25 avec Docker Compose v5.6.1 et Docker Engine 29.9.2 sous Docker Desktop (macOS, noyau `linuxkit`), sur des conteneurs `busybox:1.36`. Ils ne valent que pour cette version et cette plateforme. Ils n'ont pas été rejoués sur Docker Engine Linux natif.

## 1. Séquencement des étapes

### `depends_on` et `service_completed_successfully`

- La forme longue de `depends_on` accepte `condition: service_completed_successfully` : « a dependency is expected to run to successful completion before starting a dependent service ». Les autres conditions sont `service_started` et `service_healthy`. `required: false` (Compose v2.20.0) réduit une dépendance manquante à un avertissement. ([workload_spec.md, depends_on](https://github.com/compose-spec/compose-spec/blob/main/workload_spec.md#depends_on))
- Compose crée les services dans l'ordre des dépendances et les supprime dans l'ordre inverse. (même source)
- La politique `restart` par défaut est `no` : « It does not restart the container under any circumstances ». ([05-services.md, restart](https://github.com/compose-spec/compose-spec/blob/main/05-services.md#restart))
- Un mainteneur de Compose rappelle que Compose s'attend à des services de longue durée, et que `service_completed_successfully` est « a very limited step » vers des conteneurs d'initialisation. ([docker/compose#11808](https://github.com/docker/compose/issues/11808))

### Étape en échec *(observé, v5.6.1)*

Chaîne d'essai : `select` → `agent` → `publish`, chaque lien en `service_completed_successfully`, `agent` sortant en 3.

| Commande | Comportement observé | Code de sortie |
|---|---|---|
| `up` | `service "agent" didn't complete successfully: exit 3` ; `publish` n'est pas lancé | 1 |
| `up --exit-code-from publish` | même message, puis **attente sans fin** : `publish` reste `Created` ; interrompu au bout de 5 min | aucun (130 après SIGTERM) |
| `up --exit-code-from publish --abort-on-container-failure` | même attente sans fin ; interrompu au bout de 2 min | aucun |
| `run --rm publish` | `run` lance les dépendances, s'arrête sur l'échec, `publish` n'est pas lancé | 1 |

Chaîne réussie :

| Commande | Code de sortie |
|---|---|
| `up` | 0 |
| `up --exit-code-from publish` | 0 (la sortie en 0 de `select` puis d'`agent` n'a pas déclenché l'arrêt anticipé) |
| `up --exit-code-from publish --abort-on-container-failure` | 0 ; `publish` sortant en 5 donne 5 |
| `run --rm publish` | 0 |

Aucune issue de docker/compose décrivant l'attente sans fin de `--exit-code-from` après l'échec d'une dépendance n'a été trouvée.

### Commandes de lancement et code de sortie (documentation)

- `up` : « If the process encounters an error, the exit code for this command is `1`. If the process is interrupted using `SIGINT` (ctrl + C) or `SIGTERM`, the containers are stopped, and the exit code is `0`. » ([compose up](https://docs.docker.com/reference/cli/docker/compose/up/))
  - *(observé)* Après un SIGTERM, `up` a rendu 130, et non 0.
- `--abort-on-container-exit` : « Stops all containers if any container was stopped. » `--abort-on-container-failure` : « Stops all containers if any container exited with failure. » Les deux sont incompatibles avec `-d`. (même source)
- `--exit-code-from SERVICE` : « Return the exit code of the selected service container. Implies --abort-on-container-exit. » (même source)
- Un mainteneur précise que `--abort-on-container-failure` existe depuis v2.27.0. Combiné à `--exit-code-from`, il remplace l'implication d'`--abort-on-container-exit` : une sortie en 0 d'un service secondaire n'arrête plus le projet. ([docker/compose#13800](https://github.com/docker/compose/issues/13800))
- `run` : « Runs a one-time command against a service ». Il démarre d'abord les services liés, sauf avec `--no-deps`. `--rm` supprime le conteneur à sa sortie. ([compose run](https://docs.docker.com/reference/cli/docker/compose/run/))
  - La page ne documente pas le code de sortie de `run`. *(observé)* `run` rend le code du conteneur visé, ou 1 si une dépendance échoue.
  - *(observé)* `run --rm` ne supprime que le conteneur visé. Les conteneurs des dépendances restent dans l'état `Exited`.
- `wait` : « Block until containers of all (or specified) services stop », avec l'option `--down-project`. ([compose wait](https://github.com/docker/compose/blob/main/docs/reference/compose_wait.md))

### Conteneurs d'initialisation : `pre_start` (Compose v5.3.0)

La spécification définit `pre_start`, une suite de conteneurs d'initialisation propre à un service ([05-services.md, pre_start](https://github.com/compose-spec/compose-spec/blob/main/05-services.md#pre_start)) :

- « Each step runs to completion, in declared order, and the service container only starts once every step has exited 0. A non-zero exit fails the bring-up of the service and its dependents. »
- Chaque étape est une spécification de conteneur complète. Ce qu'elle ne déclare pas, elle l'hérite du service : image, `environment` (les valeurs déclarées complètent ou remplacent celles héritées), volumes. Une étape peut monter en écriture un volume que le service monte en lecture seule. Les règles de fusion sont celles des fichiers d'override, balises `!reset` et `!override` comprises.
- `depends_on`, `healthcheck` et `ports` ne peuvent pas être déclarés sur une étape.
- Une étape déjà réussie n'est pas rejouée par un `up` suivant, sauf si sa définition change, si son exécution précédente a échoué, ou si le conteneur du service est recréé.
- La PR [docker/compose#14093](https://github.com/docker/compose/pull/14093) (livrée en v5.6.0) fait hériter aux étapes `pre_start` toute la spécification de conteneur du service, et permet le montage en écriture d'un volume monté en lecture seule par le service (correctifs de #13939 et #13934).
- En cas d'échec, Compose garde le conteneur de l'étape pour inspection. Le `up` suivant ou `down` le supprime. ([compose up](https://docs.docker.com/reference/cli/docker/compose/up/), [compose down](https://docs.docker.com/reference/cli/docker/compose/down/))
- *(observé)* Une étape en échec donne `service "chain" pre_start[1] exited with code 3 (hook container … retained for inspection)` et un code de sortie 1 pour `up`. Les conteneurs des étapes réussies disparaissent. La sortie des étapes n'apparaît ni dans la sortie attachée de `up` ni dans `compose logs`. Seul `docker logs` sur le conteneur retenu d'une étape en échec la montre.

### Jobs (spécification de `main`, support partiel depuis v5.6.0)

- La spécification ajoute un élément de premier niveau `jobs` : « A job is a container that runs to completion ». Il est ajouté le 2026-10-14 par le commit [8f2dcf9](https://github.com/compose-spec/compose-spec/commit/8f2dcf99). ([jobs.md](https://github.com/compose-spec/compose-spec/blob/main/jobs.md))
  - Un job peut dépendre de services et d'autres jobs par `depends_on`. Un service ne peut pas dépendre d'un job.
  - `triggers.manual` (vrai par défaut) autorise le déclenchement par `run`. `triggers.schedule` prend des expressions crontab, avec `timezone`, `concurrency` (`forbid` par défaut, ou `queue`) et `missed_fires` (`one` par défaut, ou `skip`).
- Compose v5.6.0 : « partial support for jobs: only manually-triggered jobs are supported for now, scheduled jobs are not yet available. Full job support will land once the corresponding Docker Engine support is merged. » ([notes de v5.6.0](https://github.com/docker/compose/releases/tag/v5.6.0))
  - `docker compose run <job>` exécute un job comme un service : ses dépendances démarrent d'abord et son code de sortie remonte. `up` avertit des jobs déclarés et échoue sur un job planifié actif : `scheduled jobs are not supported in this version`. ([docker/compose#14093](https://github.com/docker/compose/pull/14093))
  - Les commandes `stop`, `kill`, `pause`, `logs`, `rm`, `down`, `ps`, `events` et `start` refusent un nom de job : `job ... can only be triggered with "docker compose run"`. ([docker/compose#14249](https://github.com/docker/compose/issues/14249))
- La planification par le moteur fait l'objet d'une PR en brouillon, [docker/compose#14155](https://github.com/docker/compose/pull/14155) : « it targets an experimental engine capability (gated behind a feature flag) that is not yet part of a released Docker Engine ».
- *(observé, v5.6.1)*
  - `run --rm <job>` enchaîne les jobs dépendants et rend 0, ou 1 si une dépendance échoue. Seule la sortie du job visé est affichée.
  - `compose logs <job>` est refusé avec le message ci-dessus, mais `docker logs` sur le conteneur fonctionne.
  - `down -v` laisse en place les conteneurs des jobs dépendants : `down -v --remove-orphans` les supprime.
  - Sur un projet qui n'a que des jobs, `up` rend 1 (`no service selected`).

## 2. Volumes éphémères

- Un volume monté par plusieurs services doit être un volume nommé, déclaré dans `volumes` au premier niveau. ([container_spec.md, volumes](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md#volumes), [07-volumes.md](https://github.com/compose-spec/compose-spec/blob/main/07-volumes.md))
- **Lecture seule par service** : en forme courte, `VOLUME:CHEMIN:ro` ; en forme longue, `read_only: true`. Le mode se fixe à chaque montage, si bien qu'un même volume peut être `ro` dans un service et `rw` dans un autre. ([container_spec.md, volumes](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md#volumes)) *(observé)* L'écriture dans un volume monté `:ro` échoue (`Read-only file system`) et le service voisin y écrit.
- **Volumes anonymes** : « Anonymous volumes aren't reused or shared between containers automatically. » Ils survivent à la suppression du conteneur, sauf avec `--rm`. ([Engine, Volumes](https://docs.docker.com/engine/storage/volumes/#named-and-anonymous-volumes))
- `down` ne supprime pas les volumes anonymes, mais « as they don't have a stable name, they are not automatically mounted by a subsequent `up` ». ([compose down](https://docs.docker.com/reference/cli/docker/compose/down/))
- `up -V` / `--renew-anon-volumes` : « Recreate anonymous volumes instead of retrieving data from the previous containers ». ([compose up](https://docs.docker.com/reference/cli/docker/compose/up/))
- `down -v` : « Remove named volumes declared in the "volumes" section of the Compose file and anonymous volumes attached to containers ». ([compose down](https://docs.docker.com/reference/cli/docker/compose/down/)) Sans `-v`, un volume nommé persiste d'une exécution à l'autre *(observé)*.
- Issue ouverte : quand deux projets déclarent un volume avec le même `name:` explicite, `down -v` dans l'un supprime le volume de l'autre. ([docker/compose#14373](https://github.com/docker/compose/issues/14373))
- **tmpfs** : l'attribut `tmpfs` accepte les options `mode`, `uid` et `gid`. La forme longue de `volumes` (`type: tmpfs`) accepte `size` et `mode`. ([container_spec.md, tmpfs](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md#tmpfs)) Un tmpfs appartient à un seul conteneur. Il ne peut donc pas porter des données d'une étape à la suivante. ([Engine, tmpfs mounts](https://docs.docker.com/engine/storage/tmpfs/))

## 3. Durée maximale d'une exécution

- Ni la spécification Compose ni la référence des commandes ne définissent de durée maximale pour un service, un job ou une exécution entière. Les attributs voisins portent sur autre chose :
  - `stop_grace_period` est le délai entre le signal d'arrêt et SIGKILL, 10 s par défaut ([container_spec.md](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md#stop_grace_period)) ;
  - `up --timeout` est le délai d'arrêt des conteneurs ;
  - `up --wait-timeout` borne l'attente de l'état running ou healthy ([compose up](https://docs.docker.com/reference/cli/docker/compose/up/)).
- Docker Engine n'a pas non plus d'option de durée maximale d'exécution d'un conteneur : `--stop-timeout` est un délai d'arrêt. ([docker container run](https://github.com/docker/cli/blob/master/docs/reference/commandline/container_run.md#stop-timeout))
- Mécanismes hors Compose documentés :
  - GNU `timeout` envoie TERM au bout de la durée, puis KILL avec `--kill-after`. Il rend 124 quand la commande dépasse la durée. ([coreutils, timeout invocation](https://www.gnu.org/software/coreutils/manual/html_node/timeout-invocation.html))
  - systemd : `RuntimeMaxSec=` « does not have any effect on Type=oneshot services […] (use TimeoutStartSec= to limit their activation) ». ([systemd.service](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html))
- *(observé)* Ce que devient une exécution quand le client Compose reçoit SIGTERM dépend de la commande :
  - `up` attaché arrête les conteneurs (le conteneur en cours finit `Exited (137)`) et rend 130 ;
  - `run --rm` rend 130, mais **son conteneur continue de tourner** jusqu'à sa fin normale.

## 4. Durcissement

Attributs de la spécification ([container_spec.md](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md)) :

- `read_only` : « configures the service container to be created with a read-only filesystem ».
- `tmpfs` : système de fichiers temporaire, avec `mode`, `uid` et `gid`.
- `user` : remplace l'utilisateur de l'image, root par défaut si l'image n'en fixe pas.
- `cap_drop` : capacités Linux à retirer.
- `security_opt` : options de sécurité passées au moteur.
  - Côté Engine, `no-new-privileges=true` : « Disable container processes from gaining new privileges ». ([docker container run, security-opt](https://github.com/docker/cli/blob/master/docs/reference/commandline/container_run.md#security-opt))
  - La racine en lecture seule laisse écrire dans les volumes montés. ([docker container run, read-only](https://github.com/docker/cli/blob/master/docs/reference/commandline/container_run.md#read-only))

*(observé, Docker Desktop macOS)* Conteneur lancé avec `read_only: true`, `user: "65532:65532"`, `cap_drop: [ALL]`, `security_opt: ["no-new-privileges:true"]` et `tmpfs: ["/tmp:uid=65532,gid=65532"]` :

- `id` donne `uid=65532 gid=65532` ;
- l'écriture sur `/` échoue, `/tmp` est accessible en écriture et appartient à 65532 ;
- `/proc/self/status` donne `CapEff: 0000000000000000`, `NoNewPrivs: 1` et `Seccomp: 2` (filtre seccomp actif sans configuration explicite).

**Limites sous Docker Desktop** :

- Docker Desktop exécute les conteneurs dans une VM Linux. Enhanced Container Isolation (« Prevent containers from breaching the Linux VM ») demande un abonnement Docker Business. Sur Mac, le partage de fichiers hôte passe par VirtioFS ou gRPC FUSE. ([Docker Desktop settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/))
- Aucune source primaire trouvée ne décrit une différence de comportement de `read_only`, `cap_drop`, `user` ou `no-new-privileges` entre Docker Desktop et Docker Engine sous Linux.

## 5. Logs d'un conteneur ponctuel

- Le pilote par défaut est `json-file`, sans rotation par défaut. La documentation conseille `local`, qui garde 100 Mo par conteneur avec rotation et compression. ([Configure logging drivers](https://docs.docker.com/engine/logging/configure/), [local](https://docs.docker.com/engine/logging/drivers/local/))
- Avec un pilote distant (`syslog`, `journald`, `gelf`, `fluentd`, `awslogs`, `splunk`, `gcplogs`…), les logs quittent l'hôte du conteneur. Un cache local (« dual logging ») permet alors de garder `docker logs`. ([Configure logging drivers](https://docs.docker.com/engine/logging/configure/), [dual logging](https://docs.docker.com/engine/logging/dual-logging/))
- L'attribut `logging` fixe le pilote et ses options par service. ([container_spec.md, logging](https://github.com/compose-spec/compose-spec/blob/main/container_spec.md#logging))
- `down` supprime les conteneurs des services du projet. ([compose down](https://docs.docker.com/reference/cli/docker/compose/down/))
- *(observé)* Après `down`, `compose logs` ne rend rien, avec le code 0 : les logs locaux disparaissent avec le conteneur. Aucune source primaire trouvée ne l'énonce explicitement pour `json-file` ou `local`.
- Un `up` suivant sur une configuration inchangée recrée ou redémarre les conteneurs ponctuels ([docker/compose#11808](https://github.com/docker/compose/issues/11808)). L'effet sur les logs de l'exécution précédente sans `down` n'a pas été vérifié.
- Issue ouverte : la sortie des hooks `post_start` et `pre_stop` est perdue en `up -d` et `up --wait`, et rien ne la conserve. ([docker/compose#14204](https://github.com/docker/compose/issues/14204))

## 6. Une seule passe à la fois

- La documentation de Compose ne décrit aucun verrou par projet ni aucune politique de concurrence pour `up` ou `run`. Le nom de projet vient de `-p`, de `COMPOSE_PROJECT_NAME`, de `name:` ou du nom du répertoire. ([docker compose, -p](https://docs.docker.com/reference/cli/docker/compose/#use--p-to-specify-a-project-name))
- La seule politique de concurrence de la spécification est `triggers.schedule[].concurrency` des jobs planifiés (`forbid` par défaut). Aucune release ne l'implémente. ([jobs.md](https://github.com/compose-spec/compose-spec/blob/main/jobs.md), [notes de v5.6.0](https://github.com/docker/compose/releases/tag/v5.6.0))
- *(observé)*
  - Un second `up` lancé pendant le premier, sur le même projet, ne crée pas de deuxième exécution : il s'attache au conteneur déjà en cours et rend 0 avec le premier.
  - Deux `run --rm` concurrents du même service créent deux conteneurs distincts (`<service>-run-<id>`) et s'exécutent en parallèle, sans erreur.

## Ce qui n'a pas pu être établi

- Si l'attente sans fin de `up --exit-code-from` après l'échec d'une dépendance est un défaut connu, voulu ou propre à v5.6.1. Aucune issue trouvée.
- L'écart entre la documentation de `up` (code 0 après SIGTERM) et le 130 observé.
- Le comportement de ces essais sous Docker Engine Linux natif, et de `user`, `read_only` et des volumes avec Enhanced Container Isolation activée.
- Si `docker compose run` exécute les étapes `pre_start` du service visé : non documenté, non essayé.
- La date à laquelle les jobs planifiés deviendront utilisables : ils dépendent d'une fonction de Docker Engine non publiée ([docker/compose#14155](https://github.com/docker/compose/pull/14155)).
- Une source primaire qui énonce le sort des logs `json-file` et `local` à la suppression du conteneur.
- Le comportement de `down -v` sur un volume encore attaché aux conteneurs orphelins des jobs : observé une seule fois, sans vérification.
