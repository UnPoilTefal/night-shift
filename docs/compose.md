# Running night-shift with Docker Compose

[`examples/compose/`](../examples/compose/) runs the solo tier on a single Docker host, without Kubernetes. Use it for a laptop, a NAS or a VPS ([ADR 0013](adr/0013-exemple-compose-second-systeme-d-execution.md)).

The stack runs the same steps of the same binary as the [`CronJob`](../examples/solo/cronjob.yaml): `select`, `agent`, `publish`, `retry-agent` and `retry-publish`. Each step is a long-lived service, and the steps hand over to one another in **relay mode**.

The stack adds two services:

- `control`, the **control service**, which triggers passes and keeps their history ([`docs/control.md`](control.md)).
- `proxy`, a Squid proxy that is the agents' only way out.

The Docker daemon supervises everything (`restart: unless-stopped`). There is no Docker socket, no systemd and no host cron. The example has no script and no logic of its own.

A static test ([`compose_test.go`](../examples/compose/compose_test.go)) checks `compose.yaml`, `squid.conf` and the allow-lists. It needs no Docker. It fails on any change that weakens a guarantee, and on any step option the binary does not know. An end-to-end test of the running stack is not there yet ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)).

## Requirements

- **Linux**: Docker Engine **28 or later**, with Compose v2. Engine 28 is the first version that can isolate the agents' network from the host (`gateway_mode_ipv4: isolated`).
- **Docker Desktop**: fine for trying the stack, not for running it for real. See [Docker Desktop](#docker-desktop).
- **Images**: relay mode and `control` came after release `0.2.0`, so no published image has them yet. Until the next release, build the images from `main` with `make images`, which tags them `dev`, the stack's default. After a release, set `NIGHT_SHIFT_VERSION` to it.

## Layout

| Service | Networks | Secrets | Volumes |
|---|---|---|---|
| `control` | `control-net`, `admin-net` | `control_token` | `history`, `transcripts` (purge only) |
| `select` | `trusted-net`, `control-net` | `forge_token` | `state`, `work`, `state2`, `work2` (emptied before each pass) |
| `agent` | `agent-net` | `model_token` | `state` (read-only), `work` |
| `publish` | `trusted-net`, `control-net` | `forge_token`, `discord_webhook` | `state` and `work` (read-only), `state2`, `work2`, `transcripts` |
| `retry-agent` | `agent-net` | `model_token` | `state2` (read-only), `work2` |
| `retry-publish` | `trusted-net`, `control-net` | `forge_token`, `discord_webhook` | `state2` and `work2` (read-only), `transcripts` |
| `proxy` | `agent-net`, `trusted-net` | none | `squid.conf` and `allowlist/` (read-only) |

The networks keep each service to what it needs:

- **`agent-net`** is internal, with no address on the host. The agents can only reach the proxy, and the proxy only lets `CONNECT` through to port 443 of the domains in its lists. An agent resolves no public name, and reaches neither `control` nor any host service.
- **`trusted-net`** is a normal network: the trusted steps reach the forge and Discord directly.
- **`control-net`** is internal. It links `control` to the trusted steps only, and the steps API has no authentication of its own.
- **`admin-net`** carries a single published port: `127.0.0.1:8081` on the host, `control`'s read-only administration API. Docker publishes no port for a container attached only to internal networks (observed on Docker Desktop 29.9), which is why `control` has this extra network.

The agents have no persistent volume. Their `HOME`, their `CLAUDE_CONFIG_DIR` and their `/tmp` are `tmpfs`, and the agent empties them after each round. It runs as PID 1 (`init: false`) and kills every other process it started. Transcripts reach the `transcripts` volume only through `publish`.

Every service runs with a read-only root filesystem, `cap_drop: [ALL]` and `no-new-privileges`, as UID 65532. The proxy is the one exception, described in [the guarantee gaps](#gaps-from-the-cronjob). Logs go to `json-file`, 10 MB × 5 per service.

## Configuration

Compose reads these variables from the environment or from a `.env` file next to `compose.yaml`. `.env` is ignored by git.

| Variable | Default | Meaning |
|---|---|---|
| `NIGHT_SHIFT_REPO` | required | Candidate repository: `owner/repo` on GitHub, `group/subgroup/project` on GitLab |
| `NIGHT_SHIFT_FORGE` | `github` | Forge adapter: `github` or `gitlab` |
| `NIGHT_SHIFT_INSTANCE` | empty | Base address of a dedicated or self-managed instance, for example `https://gitlab.example.com`. Leave it empty for github.com or gitlab.com |
| `NIGHT_SHIFT_SCHEDULE` | `0 2 * * *` | Cron schedule of the passes, in UTC |
| `NIGHT_SHIFT_VERSION` | `dev` | Tag of the `night-shift` and `night-shift-go` images |
| `NIGHT_SHIFT_SECRETS_DIR` | `./secrets` | Directory of the secret files |

On GitLab, for example:

```sh
NIGHT_SHIFT_FORGE=gitlab
NIGHT_SHIFT_INSTANCE=https://gitlab.example.com
NIGHT_SHIFT_REPO=group/subgroup/project
```

The token rights are those of the `CronJob`: see the README, and [`docs/gitlab.md`](gitlab.md) for GitLab.

## Secrets

Secrets are files, never environment variables, so they appear neither in `docker inspect` nor in `docker compose config`. Compose mounts each one read-only under `/run/secrets/` of the services that need it. Each step then reads it through the `…_FILE` form of its variable.

| File | Mounted in | Content |
|---|---|---|
| `forge_token` | `select`, `publish`, `retry-publish` | Forge token |
| `discord_webhook` | `publish`, `retry-publish` | Discord webhook URL. An empty file means no notification |
| `model_token` | `agent`, `retry-agent` | `claude setup-token` token. For an API key, switch both agents to `--auth api-key` and `ANTHROPIC_API_KEY_FILE` |
| `control_token` | `control` | Token of the administration API |

```sh
mkdir -m 700 secrets
printf '%s' "$FORGE_TOKEN" > secrets/forge_token
printf '%s' "$MODEL_TOKEN" > secrets/model_token
openssl rand -hex 32 > secrets/control_token
: > secrets/discord_webhook          # or the webhook URL
chmod 600 secrets/*
```

All four files must exist: Compose refuses to start a stack whose secret file is missing. `secrets/` is ignored by git.

**Readability.** Outside Swarm, Compose ignores the `uid`, `gid` and `mode` of a file secret: it bind-mounts the host file as is. The services run as UID 65532, so on Linux each file must be readable by that UID. For example, `sudo chown 65532:65532 secrets/* && sudo chmod 400 secrets/*`. A step that cannot read its secret says so and names the variable, never the content. This readability has to be checked on a real Linux host; the end-to-end test will check it ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)).

## Running

```sh
cd examples/compose
docker compose up -d
docker compose logs -f select publish
```

Passes run on schedule. To request one now:

```sh
docker compose exec control night-shift control trigger
```

Requests received while a pass is running merge into a single waiting pass. Only one pass runs at a time, and the watchdog (2 hours in the example) closes a stuck pass so the next one can start.

## History

`control` keeps a record of each closed pass in the `history` volume: tickets with their outcome, repositories left out and detailed incidents. The record never leaves the host. The digest on the forge only counts incidents. History and archived transcripts are purged after 30 days.

Read the history through the administration API:

```sh
curl -s -H "Authorization: Bearer $(cat secrets/control_token)" http://127.0.0.1:8081/v1/admin/passes
```

The routes are in [`docs/control.md`](control.md).

## Adapting the allow-lists

The proxy configuration, [`squid.conf`](../examples/compose/squid.conf), is frozen. Only the lists in [`allowlist/`](../examples/compose/allowlist/) change, one domain per line:

- `model.txt`: the model provider, `api.anthropic.com`;
- `toolchain.txt`: the agent's package registries, `proxy.golang.org` and `sum.golang.org` for Go.

For a repository in another language, put that language's registries in `toolchain.txt`. The static test refuses:

- a whole top-level domain (`.com`);
- any forge domain (`github.com`, `gitlab.com`…), because the agent never talks to the forge.

The agents' `GOPROXY` has no `direct` fallback, so a module the Go proxy lacks fails instead of reaching its origin repository.

Which hosts `claude -p` reaches with a subscription token beyond `api.anthropic.com` has not been checked yet. If a pass fails on a refused `CONNECT`, the proxy logs show the domain: `docker compose logs proxy | grep TCP_DENIED`.

## Private LLM gateway

The proxy refuses private and reserved address ranges before any allow rule, so an allowed name that resolves to your local network leads nowhere. A private LLM gateway gets **one** exception, which requires both its exact domain and its exact address. Add it to `squid.conf` just before `http_access deny private_dst`, as the commented template there shows:

```
acl llm_gateway_name dstdomain llm.internal.example
acl llm_gateway_addr dst 10.20.30.40/32
http_access allow CONNECT SSL_ports llm_gateway_name llm_gateway_addr
```

This is the only change to `squid.conf` that the static test accepts: a single exception, with one exact domain and one `/32` or `/128` address.

## Docker Desktop

Docker Desktop runs the containers in a VM, which changes some guarantees:

- `gateway_mode_ipv4: isolated` is not documented there: an agent may still reach the VM's gateway.
- File secrets are bind-mounted through Desktop's file sharing, so their `mode` on the host does not apply.

The proxy starts there with this configuration and hardening: from `agent-net`, a `CONNECT` to `api.anthropic.com` gets its tunnel, one to an unlisted domain gets `403` and a `TCP_DENIED` line in `docker compose logs proxy`, and a direct connection fails at name resolution. That was checked on Docker Desktop 29.9 on 2026-10-27. Keep Desktop for trying the stack, and run it for real on Linux.

## Gaps from the `CronJob`

The stack keeps the `CronJob`'s guarantees: the agent holds no forge token, its only way out is the proxy, a container boundary separates the steps, and transcripts stay out of the agent's reach. These gaps remain ([ADR 0013](adr/0013-exemple-compose-second-systeme-d-execution.md)):

| Gap | Checked by |
|---|---|
| The agent container lives from one pass to the next. After each round, the binary, as PID 1, kills every other process and empties its `tmpfs` (`HOME`, `CLAUDE_CONFIG_DIR`, `/tmp`). | Scenario 6 of the end-to-end test ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)): a hostile fake agent leaves a process and files behind, and nothing survives to the next pass. |
| The agent's end is a marker the agent can forge. `publish` copies the agent's output, then validates and pushes only its copy. | Scenario 7 ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)): a fake agent announces its end, then rewrites its output. |
| The proxy image's entrypoint starts as root, with only `SETUID` and `SETGID`, then Squid switches to the `proxy` user. | The static test; then the maintainer's run on a real host. |
| Docker Desktop guarantees neither the `isolated` mode of the agents' network nor the `mode` of secret files. | This guide: Docker Desktop is for trying, not deploying. |
| Whether UID 65532 can read the secrets depends on the host, and has to be checked on Linux. | Scenario 5 ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)). |
| `control` needs a non-internal network (`admin-net`) to publish its administration API on `127.0.0.1`. That network gives it a route out, which it never uses: the binary makes no outbound call. | The static test: `admin-net` carries `control` alone, and only `127.0.0.1:8081` is published. |
