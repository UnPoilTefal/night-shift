# Compose : fermer la sortie réseau de l'agent

Constat de recherche pour le ticket [#92](https://github.com/UnPoilTefal/night-shift/issues/92), enfant de la carte [#90](https://github.com/UnPoilTefal/night-shift/issues/90). Relevé au 2026-10-25.

**Question** : sous Docker Compose v2, sur un hôte Docker unique, comment garantir que le conteneur de l'agent ne peut sortir que par un proxy, même s'il ignore `HTTPS_PROXY` ?

Ce document rassemble des faits sourcés. Il ne recommande aucune conception. Les points que les sources consultées n'ont pas permis d'établir sont signalés par **Non établi**.

## 1. Réseau interne (`internal: true`)

### Ce que la documentation garantit

- Compose fournit par défaut une connectivité externe aux réseaux ; `internal: true` crée « an externally isolated network ». [Compose file reference, networks](https://docs.docker.com/reference/compose-file/networks/)
- Le guide réseau de Compose décrit un réseau interne comme un réseau « without a connection to the host's network interfaces ». Un service branché à la fois sur un réseau interne et sur un réseau non interne peut sortir par ce dernier. [Networking in Compose](https://docs.docker.com/compose/how-tos/networking/)
- Côté Engine, `docker network create --internal` : « Containers on an internal network may communicate between each other, but not with any other network, as no default route is configured and firewall rules are set up to drop all traffic to or from other networks. » [docker network create](https://docs.docker.com/reference/cli/docker/network/create/)
- Le constat ne dépend donc pas de la coopération du processus : l'absence de route par défaut et les règles de pare-feu s'appliquent quel que soit le respect de `HTTPS_PROXY` par le programme. (Déduction directe des deux citations précédentes.)

### Limite : accès à l'hôte par la passerelle

- La même page précise : « Communication with the gateway IP address (and thus appropriately configured host services) is possible, and the host may communicate with any container IP directly. » [docker network create](https://docs.docker.com/reference/cli/docker/network/create/)
- La page sur la publication de ports le détaille : « An address is normally assigned to the bridge device in an `internal` network », et « containers in the network can access host services listening on that bridge address ». [Port publishing](https://docs.docker.com/engine/network/port-publishing/)
- Depuis Docker Engine 28.0.0, les options `com.docker.network.bridge.gateway_mode_ipv4` et `gateway_mode_ipv6` acceptent le mode `isolated`, seulement pour un réseau aussi `internal` : « An `internal` bridge network created with gateway mode `isolated` does not have an address on the Docker host. » [Engine 28 release notes](https://docs.docker.com/engine/release-notes/28/) ; « No address is assigned to the bridge when the network is created with gateway mode `isolated`. » [Port publishing](https://docs.docker.com/engine/network/port-publishing/)
- **Non établi** : la syntaxe Compose exacte pour passer ce mode (vraisemblablement `driver_opts` du réseau) n'est pas illustrée dans les pages consultées, et son comportement sous Docker Desktop n'est pas documenté.

### Limite : DNS

- Les conteneurs d'un réseau personnalisé utilisent le serveur DNS embarqué de Docker, à l'adresse `127.0.0.11`, qui « forwards external DNS lookups to the DNS servers configured on the host ». [Networking overview](https://docs.docker.com/engine/network/)
- Faille CVE-2024-29018 (avis [GHSA-mq39-4gv4-mvpx](https://github.com/moby/moby/security/advisories/GHSA-mq39-4gv4-mvpx)) : sur un réseau interne, le démon relayait les requêtes DNS vers un serveur externe depuis l'espace réseau de l'hôte, ce qui ouvrait un canal d'exfiltration par requêtes DNS. Le comportement voulu est qu'un conteneur rattaché seulement à un réseau interne ne puisse pas résoudre de noms externes, seulement ceux des conteneurs du même réseau. Corrigé dans Moby 26.0.0-rc3, 25.0.5 et 23.0.11. Docker Desktop n'était pas affecté, « as Docker Desktop always runs an internal resolver on a RFC 1918 address ».
- Conséquence factuelle : sur un Engine corrigé, un conteneur seulement interne ne résout pas les noms publics ; seul le proxy, s'il a une patte externe, résout les noms de destination (voir section 2).
- **Non établi** : la documentation Docker ne décrit nulle part, hors de l'avis de sécurité, le comportement du DNS embarqué sur un réseau interne.

### IPv6

- L'IPv6 s'active explicitement par réseau (`--ipv6`, ou `enable_ipv6: true` en Compose) ; « IPv6 is only supported on Docker daemons running on Linux hosts » et ip6tables « is enabled by-default ». [Use IPv6 networking](https://docs.docker.com/engine/daemon/ipv6/) ; [Compose networks](https://docs.docker.com/reference/compose-file/networks/)
- **Non établi** : aucune page consultée ne dit explicitement qu'un réseau utilisateur sans `enable_ipv6` n'a aucune adresse IPv6 ; les pages montrent seulement l'activation explicite.

### Pare-feu de l'hôte

- Docker crée les règles de pare-feu des réseaux bridge ; la chaîne `DOCKER-USER` reçoit les règles utilisateur avant `DOCKER-FORWARD` et `DOCKER` ; une chaîne `DOCKER-INTERNAL` existe parmi les chaînes de décision. Désactiver `iptables`/`ip6tables` dans le démon « will likely break container networking ». [Docker with iptables](https://docs.docker.com/engine/network/firewall-iptables/) ; [Packet filtering and firewalls](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
- Un backend nftables existe, encore expérimental, et il n'active pas lui-même le routage IP. [Packet filtering and firewalls](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
- **Non établi** : le détail des règles posées pour un réseau interne (contenu de `DOCKER-INTERNAL`) n'est pas documenté dans les pages consultées.

## 2. Proxy à deux pattes

- Un conteneur peut être relié à plusieurs réseaux, comme un hôte à plusieurs réseaux Ethernet. [Networking overview](https://docs.docker.com/engine/network/) En Compose, chaque service liste ses réseaux ; l'exemple documenté relie `app` à `frontend` et `backend`, et seul `app` parle aux deux. [Networking in Compose](https://docs.docker.com/compose/how-tos/networking/) ; [Compose networks](https://docs.docker.com/reference/compose-file/networks/)
- Le schéma qui en découle : l'agent seulement sur un réseau interne, le proxy sur ce réseau interne et sur un réseau non interne. Le proxy sort par son réseau non interne (dernier point de la section « Ce que la documentation garantit »).
- **Passerelle par défaut d'un conteneur multi-réseaux** : « The default gateway is selected by Docker, and may change whenever a container's network connections change. » L'option `gw-priority` (défaut `0`) désigne le réseau dont la passerelle l'emporte. [Networking overview](https://docs.docker.com/engine/network/) Un réseau interne n'a pas de route par défaut (section 1), donc la sortie du proxy passe par son réseau externe.
- **Résolution DNS côté proxy** : l'agent seulement interne ne résout pas les noms publics (section 1, DNS). Avec `CONNECT`, le client envoie le nom d'hôte au proxy, qui le résout lui-même. Squid utilise par défaut les serveurs de `/etc/resolv.conf` (directive `dns_nameservers`). [Squid dns_nameservers](https://www.squid-cache.org/Doc/config/dns_nameservers/) Le client ne doit donc pas avoir besoin de résoudre la destination ; il doit seulement résoudre le nom du proxy, ce que le DNS embarqué fait pour les noms de conteneurs du même réseau. [Networking in Compose](https://docs.docker.com/compose/how-tos/networking/)
- **Piège de la résolution par le proxy** : un proxy de transfert dynamique joint toute adresse que ses DNS lui donnent. La documentation d'Envoy l'écrit : « any address reachable from the proxy is potentially accessible by untrusted clients », y compris localhost, adresses link-local, serveurs de métadonnées et réseau privé, et des enregistrements DNS non fiables peuvent pointer vers elles. [Envoy dynamic forward proxy filter](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/dynamic_forward_proxy_filter) Smokescreen vérifie pour cette raison que l'adresse résolue est publique. [stripe/smokescreen](https://github.com/stripe/smokescreen)
- **Piège IPv6** : Squid contacte les sites en IPv6 par défaut ; la directive `dns_v4_first`, qui inversait cette préférence, n'existe plus depuis Squid v5. [Squid dns_v4_first](https://www.squid-cache.org/Doc/config/dns_v4_first/) **Non établi** : le comportement exact de repli de Squid 6/7 quand le réseau externe du proxy n'a pas de route IPv6.
- **Piège des variables injectées** : le client Docker injecte dans chaque nouveau conteneur les variables `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY` (et minuscules) définies dans `~/.docker/config.json`. [Configure Docker to use a proxy server](https://docs.docker.com/engine/cli/proxy/) Ce mécanisme ne fait qu'écrire des variables ; il n'impose rien au réseau. Il peut entrer en concurrence avec les variables qu'une Composition définit. **Non établi** : laquelle l'emporte quand le service Compose définit aussi ces variables.

## 3. Proxys candidats (liste d'autorisation par nom d'hôte, `CONNECT` sans déchiffrement)

| Proxy | Image | Liste d'autorisation | Journal des refus |
|---|---|---|---|
| Squid | `ubuntu/squid`, maintenue par Canonical (éditeur vérifié) ; étiquettes `7.2-26.04_beta`/`latest`, `6.6-24.04_beta`, `5.2-22.04_beta` ; configuration en `/etc/squid/squid.conf`, port 3128, journaux sous `/var/log/squid`. [Docker Hub ubuntu/squid](https://hub.docker.com/r/ubuntu/squid) Le projet Squid ne publie pas d'image propre (**Non établi** formellement : aucune trouvée). | `acl … dstdomain .exemple.com` ; `http_access allow|deny`. Sans ligne qui corresponde, la décision est l'inverse de la dernière ligne ; la doc conseille de finir par un refus général. Exemple de base : `http_access deny CONNECT !SSL_ports` avec `acl SSL_ports port 443`. `dstdomain` tente une résolution inverse pour une URL en adresse IP. [Squid acl](https://www.squid-cache.org/Doc/config/acl/) ; [Squid http_access](https://www.squid-cache.org/Doc/config/http_access/) | `access.log` ; l'étiquette `DENIED` signifie « The request was denied by access controls ». [Squid logs FAQ](https://wiki.squid-cache.org/SquidFaq/SquidLogs) |
| tinyproxy | Dernière version 1.11.3. [Releases](https://github.com/tinyproxy/tinyproxy/releases) **Non établi** : aucune image officielle mentionnée par le projet. | `Filter` (fichier de règles), `FilterDefaultDeny Yes` (la liste devient une liste d'autorisation), `FilterType fnmatch|bre|ere` ; le filtrage porte sur le domaine par défaut, le filtrage d'URL ne vaut que pour HTTP en clair ; `ConnectPort` restreint les ports de `CONNECT`. [tinyproxy manual](https://tinyproxy.github.io/) | `LogLevel` (Critical à Info). **Non établi** : format exact d'une ligne de refus. |
| Envoy | Image officielle `envoyproxy/envoy`, variantes `distroless-…`, `contrib-…`, `debug-…`. [Envoy install](https://www.envoyproxy.io/docs/envoy/latest/start/install) | `CONNECT` désactivé par défaut (403) ; s'active par `upgrade_configs` `CONNECT` ; en HTTP/1.1 la requête se route par `connect_matcher` et c'est la cible du `CONNECT` qui est comparée aux domaines du virtual host, parfois avec le port (`hostname:port`). [Envoy HTTP upgrades](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/http/upgrades) Pour des hôtes non connus d'avance : filtre et cluster de transfert dynamique (`dns_cache_config`, `dns_lookup_family`). [Dynamic forward proxy](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/dynamic_forward_proxy_filter) **Non établi** : un exemple officiel complet de `CONNECT` + transfert dynamique + liste d'autorisation. | Journal d'accès configurable. **Non établi** : configuration minimale d'un journal de refus. |
| Smokescreen (Stripe) | `ghcr.io/stripe/smokescreen:<version>`, amd64 et arm64, utilisateur non root. [stripe/smokescreen](https://github.com/stripe/smokescreen) | Proxy `CONNECT` ; ACL YAML par service, politiques `open`, `report`, `enforce` ; listes `global_allow_list`/`global_deny_list` ; refuse par défaut les adresses résolues non publiques. L'identité du client vient par défaut du certificat mTLS. **Non établi** : fonctionnement sans mTLS avec une politique par défaut seule. | Le motif de décision figure dans les journaux et dans la réponse au client. |

Autres candidats non examinés ici : aucun autre proxy maintenu n'a été vérifié dans ses sources primaires pendant cette recherche.

## 4. Domaines nécessaires aux étapes

L'exemple `examples/solo/cronjob.yaml` enchaîne `select`, `agent`, `publish`, `retry-agent`, `retry-publish`. Le jeton de forge n'est monté que dans `select` et les `publish`, le webhook Discord que dans les `publish`.

### Étapes de confiance (`select`, `publish`, `retry-publish`)

- **Forge GitHub, API** : `https://api.github.com`, l'URL de base de l'API REST. [GitHub REST API](https://docs.github.com/en/rest/using-the-rest-api/getting-started-with-the-rest-api) C'est la valeur par défaut de l'adaptateur (`internal/forge/github/github.go`, `DefaultBaseURL`).
- **Forge GitHub, git** : les URL de clonage HTTPS ont la forme `https://github.com/user/repo.git` et « work even if you are behind a firewall or proxy ». [About remote repositories](https://docs.github.com/en/get-started/git-basics/about-remote-repositories) C'est `DefaultGitURL` dans l'adaptateur.
- GitHub déconseille les listes d'autorisation par adresse IP ; l'API `/meta` publie les adresses et une liste `domains` par catégorie (`website`, `packages`, `actions`…), avertit que ses valeurs changent et qu'elle n'est pas exhaustive. [About GitHub's IP addresses](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-githubs-ip-addresses) ; [REST meta](https://docs.github.com/en/rest/meta/meta)
- **Notification Discord** : URL de base de l'API `https://discord.com/api` ; exécution d'un webhook sur `/webhooks/{webhook.id}/{webhook.token}`. [Discord API reference](https://docs.discord.com/developers/reference) ; [Discord webhook resource](https://docs.discord.com/developers/resources/webhook) L'exemple du dépôt le confirme : le proxy doit laisser `publish` joindre `discord.com`.
- **Non établi** : si Git, en clonage HTTPS sur GitHub, est redirigé vers un autre hôte (par exemple pour LFS) ; hors du besoin d'un dépôt sans LFS.

### Conteneur de l'agent (`agent`, `retry-agent`)

- **Modèle, Claude Code** : `api.anthropic.com` porte les requêtes à l'API Claude, la vérification de domaine de WebFetch, les feature flags et la télémétrie d'événements. `platform.claude.com` porte l'échange, le rafraîchissement et la révocation des jetons OAuth, y compris pour les comptes claude.ai ; `claude.ai` l'authentification des comptes claude.ai. [Claude Code network config](https://code.claude.com/docs/en/network-config)
- **Non établi** : si l'usage d'un jeton `CLAUDE_CODE_OAUTH_TOKEN` (`claude setup-token`) en mode `-p` joint `platform.claude.com` ou `claude.ai` pendant une passe, ou seulement `api.anthropic.com`. La doc liste les hôtes par fonction, pas par mode d'authentification headless.
- Hôtes facultatifs pour l'agent : télémétrie Datadog (`http-intake.logs.us5.datadoghq.com`, `browser-intake-us5-datadoghq.com`), désactivée par `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` ; `downloads.claude.ai` (mise à jour) ; `registry.npmjs.org` (plugins, serveurs MCP par `npx`) ; `mcp-proxy.anthropic.com` (connecteurs claude.ai, désactivables par `ENABLE_CLAUDEAI_MCP_SERVERS=false`) ; `raw.githubusercontent.com` jamais joint en mode non interactif. [Claude Code network config](https://code.claude.com/docs/en/network-config) L'image de base du dépôt fixe déjà `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` et `DISABLE_AUTOUPDATER=1` (`images/base/Dockerfile`).
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` ne coupe pas la vérification de domaine de WebFetch (vers `api.anthropic.com`), qui a son propre réglage `skipWebFetchPreflight`. [Claude Code data usage](https://code.claude.com/docs/en/data-usage) WebFetch n'est pas dans les outils par défaut de l'agent (`DefaultAllowedTools`, `internal/harness/claude/claude.go`).
- Claude Code lit `https_proxy`, `HTTPS_PROXY`, `http_proxy`, `HTTP_PROXY` dans cet ordre, respecte `NO_PROXY`, ne prend pas en charge les proxys SOCKS, et arrête son lancement si l'URL du proxy est illisible. [Claude Code network config](https://code.claude.com/docs/en/network-config)
- **Registres Go** (outils `go` permis à l'agent) : `GOPROXY` vaut par défaut `https://proxy.golang.org,direct` ; `direct` signifie contacter directement le système de gestion de versions ; `off`, aucune communication. `GOSUMDB` vaut par défaut `sum.golang.org` ; le `go` ne joint pas la base de sommes directement si un proxy de `GOPROXY` la reflète. `GOPRIVATE` sert de défaut à `GONOPROXY` et `GONOSUMDB`. [Go Modules Reference](https://go.dev/ref/mod) ; [proxy.golang.org](https://proxy.golang.org/)
- Le téléchargement d'une chaîne Go (`GOTOOLCHAIN=auto`, le défaut) passe par `GOPROXY` sous le module `golang.org/toolchain` ; `GOTOOLCHAIN=local` l'interdit. [Go toolchains](https://go.dev/doc/toolchain) L'image `night-shift-go` fixe `GOTOOLCHAIN=local` (`images/go/Dockerfile`).
- Le client HTTP de Go lit `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (et minuscules) selon le schéma de la requête, sauf pour `localhost`. [net/http ProxyFromEnvironment](https://pkg.go.dev/net/http#ProxyFromEnvironment)
- **Non établi** : si `proxy.golang.org` redirige les téléchargements de zips vers un autre hôte ; la page du service n'en parle pas. Avec `direct` en repli, un module absent du proxy entraîne une connexion au dépôt d'origine (souvent `github.com`), donc une liste d'autorisation sans cet hôte fait échouer ces modules.

### Synthèse factuelle

| Domaine | Étapes de confiance | Agent |
|---|---|---|
| `api.github.com` | oui (API de forge) | non : l'agent ne détient aucun jeton |
| `github.com` | oui (git) | seulement si un module Go retombe en `direct` |
| `discord.com` | oui (`publish`) | non |
| `api.anthropic.com` | non | oui |
| `platform.claude.com`, `claude.ai` | non | possible (authentification OAuth, voir Non établi) |
| `proxy.golang.org`, `sum.golang.org` | non | oui pour un dépôt Go qui télécharge des modules |

Le partage « confiance / agent » de ce tableau vient de la répartition des secrets dans `examples/solo/cronjob.yaml`, pas d'une source externe.

## 5. Épreuve

Faits utiles pour une épreuve en ligne de commande, avec `curl` :

- `--noproxy '*'` désactive tout proxy : « The only wildcard is a single "*" character, which matches all hosts, and effectively disables the proxy. » `-x/--proxy` force un proxy et passe outre les variables d'environnement ; `-p/--proxytunnel` force un tunnel `CONNECT`. [curl man page](https://curl.se/docs/manpage.html)
- `NO_PROXY='*'` a le même effet que `--noproxy '*'` ; la variable minuscule l'emporte sur la majuscule. [curl man page](https://curl.se/docs/manpage.html)
- Codes de sortie distinctifs : 5 (proxy non résolu), 6 (hôte non résolu), 7 (connexion impossible), 28 (délai dépassé), 56 (échec de réception). [curl man page](https://curl.se/docs/manpage.html)

Épreuves qui en découlent, à lancer depuis le conteneur de l'agent (l'image doit contenir `curl`, ce que ce constat n'a pas vérifié) :

1. Sortie directe : `curl --noproxy '*' --connect-timeout 5 https://example.com` doit échouer, par un code 6 (pas de résolution sur un réseau interne corrigé) ou 7/28 (pas de route).
2. Domaine hors liste : `curl -x http://<proxy>:<port> https://example.com` doit échouer sur le refus du `CONNECT` par le proxy (Envoy répond 403 par défaut à un `CONNECT` non configuré ; [Envoy HTTP upgrades](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/http/upgrades)), et le journal du proxy doit montrer le refus.
3. Domaine en liste : la même commande vers `https://api.anthropic.com` doit établir le tunnel.
4. DNS : `getent hosts example.com` ou équivalent doit échouer dans le conteneur de l'agent, ce qui montre l'absence d'exfiltration par DNS (CVE-2024-29018).
5. Hôte : une connexion vers l'adresse de passerelle du réseau interne montre si un service de l'hôte reste joignable (section 1, passerelle).

**Non établi** : le code de sortie précis de `curl` quand un proxy refuse un `CONNECT` par 403 ; le texte de la page ne l'indique pas.

## 6. Docker Engine Linux et Docker Desktop

| Point | Docker Engine sous Linux | Docker Desktop |
|---|---|---|
| Où tourne le moteur | Sur l'hôte. | Dans une VM Linux légère ; les conteneurs vivent dans un petit réseau virtuel géré par Docker, relié par un canal en mémoire partagée. [Desktop networking](https://docs.docker.com/desktop/features/networking/) |
| DNS d'un réseau interne | Relais externe possible avant les versions corrigées de la CVE-2024-29018. | Non affecté par la CVE : résolveur interne sur une adresse RFC 1918. [GHSA-mq39-4gv4-mvpx](https://github.com/moby/moby/security/advisories/GHSA-mq39-4gv4-mvpx) |
| Accès à l'hôte | Par l'adresse du bridge d'un réseau interne, sauf mode `isolated` (Engine 28+). [Port publishing](https://docs.docker.com/engine/network/port-publishing/) | `host.docker.internal` résout l'adresse interne de l'hôte, `gateway.docker.internal` la passerelle de la VM. [Desktop networking how-tos](https://docs.docker.com/desktop/features/networking/networking-how-tos/) **Non établi** : si ces noms restent joignables depuis un réseau `internal: true`. |
| Visibilité des conteneurs depuis l'hôte | L'hôte joint toute adresse de conteneur. [docker network create](https://docs.docker.com/reference/cli/docker/network/create/) | Pas de `docker0` sur l'hôte (il est dans la VM), pas de ping ni de routage vers les conteneurs Linux. [Desktop networking how-tos](https://docs.docker.com/desktop/features/networking/networking-how-tos/) |
| IPv6 | Réservé aux démons sur hôtes Linux selon la doc IPv6 de l'Engine. [Use IPv6](https://docs.docker.com/engine/daemon/ipv6/) | Mode double pile par défaut, modes IPv4 seul et IPv6 seul ; filtrage DNS des requêtes IPv6 seulement en double pile. [Desktop networking how-tos](https://docs.docker.com/desktop/features/networking/networking-how-tos/) |
| Proxy imposé aux conteneurs | Rien d'intégré au-delà du réseau interne et des règles `DOCKER-USER`. | « Air-gapped containers » : `containersProxy` dans `admin-settings.json`, avec PAC et `transparentPorts` ; exige Docker Business, connexion imposée et Settings Management. [Air-gapped containers](https://docs.docker.com/enterprise/security/hardened-desktop/air-gapped-containers/) Le proxy de conteneurs sert toujours aux `docker pull`. [Desktop settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/) |
| Mode `isolated` | Disponible depuis Engine 28.0.0. | **Non établi** : support et effet dans la VM de Desktop. |

## Ce qui n'a pas pu être établi (récapitulatif)

- Syntaxe Compose documentée du mode de passerelle `isolated`, et son effet sous Docker Desktop.
- Comportement du DNS embarqué sur un réseau interne, hors de l'avis de sécurité.
- Absence d'IPv6 garantie sur un réseau utilisateur sans `enable_ipv6`.
- Contenu des règles `DOCKER-INTERNAL`.
- Joignabilité de `host.docker.internal` depuis un réseau interne sous Docker Desktop.
- Hôtes joints par Claude Code en `-p` avec un jeton d'abonnement, au-delà d'`api.anthropic.com`.
- Redirections éventuelles de `proxy.golang.org` vers un autre hôte.
- Image officielle de tinyproxy ; format de refus de tinyproxy et d'Envoy ; fonctionnement de Smokescreen sans mTLS.
- Repli IPv4/IPv6 de Squid 6/7 ; priorité entre variables de proxy injectées par le client Docker et variables de la Composition.
- Code de sortie de `curl` sur un `CONNECT` refusé.
