# night-shift

**Your ready tickets move forward overnight. In the morning, you review draft pull or merge requests instead of babysitting agents.**

[![CI](https://github.com/UnPoilTefal/night-shift/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/UnPoilTefal/night-shift/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/UnPoilTefal/night-shift/badge)](https://scorecard.dev/viewer/?uri=github.com/UnPoilTefal/night-shift)
[![Release](https://img.shields.io/github/v/tag/UnPoilTefal/night-shift?label=release&sort=semver)](https://github.com/UnPoilTefal?tab=packages&repo_name=night-shift)
[![Go version](https://img.shields.io/github/go-mod/go-version/UnPoilTefal/night-shift)](go.mod)
[![MIT License](https://img.shields.io/github/license/UnPoilTefal/night-shift)](LICENSE)
[![Renovate](https://img.shields.io/badge/renovate-enabled-brightgreen?logo=renovatebot)](https://github.com/UnPoilTefal/night-shift/issues/1)
[![Status](https://img.shields.io/badge/status-in%20development-yellow)](#roadmap)
[![Decisions](https://img.shields.io/badge/decisions-ADR-blue)](docs/adr/)
[![Glossary](https://img.shields.io/badge/glossary-GLOSSARY.md-blue)](GLOSSARY.md)

night-shift runs scheduled **passes** of AI coding agents over the **ready tickets** in an issue tracker: issues labelled `ready-for-agent` and carrying a self-contained agent brief. Each pass reserves a capped number of tickets and hands each one to a headless agent, locked in a container that has no privileges. It produces exactly two things: **draft change requests** (pull requests on GitHub, merge requests on GitLab) and a **digest** for human review.

This project does not bet on the model behaving. **Everything the agent must not do is enforced by a deterministic mechanism.**

## How it works

```mermaid
flowchart LR
    T["🎫 Ready tickets<br/><code>ready-for-agent</code>"]
    subgraph P["Scheduled pass (one active at a time)"]
        direction LR
        S["① Selection<br/>priority, age,<br/>trusted authors,<br/>reservation"]
        A["② Agent<br/><b>no forge token</b><br/>egress through an<br/>allow-list proxy"]
        U["③ Publication<br/>deterministic, no model<br/><b>zones checked<br/>before every push</b><br/><code>agent/…</code> branch"]
        S -- "filtered brief" --> A
        A -- "commit series<br/>(format-patch)" --> U
    end
    PR["📝 Draft PR / MR<br/><code>sensitive-zone</code> label<br/>when one is touched"]
    R["🛑 Zone refusal<br/>nothing pushed<br/><code>ready-for-human</code>"]
    H["👤 Human review<br/>+ digest"]
    T --> S
    U --> PR --> H
    U -- "forbidden zone<br/>touched" --> R --> H
```

1. **Selection**: the pass walks the queue (priority, then age, skipping blocked tickets), keeps only content written by trusted authors, and **reserves** the tickets it takes.
2. **Agent**: a container that receives a filtered brief and a clone of the repository. It holds **no forge token** and can only reach DNS and an egress proxy with a domain allow-list. It returns a series of commits, nothing more.
3. **Publication**: a step with no model involved. It applies the series, checks the zones (below), pushes an `agent/…` branch with an attribution trailer and opens a **draft** PR. It never merges and never pushes a tag. It then follows the PR's CI: if checks fail, the pass relaunches the agent **once**, in a fresh container on a fresh clone of the published branch, with the names and excerpts of the failing checks. There is never a third round.
4. **Zones**: before every push, in both rounds, Publication judges **each commit** of the series against the opt-in of the base branch, never the one in the series. If a single commit touches a **forbidden zone** (CI, release pipeline, agent configuration), nothing is pushed: this is a **zone refusal**, and the ticket goes back to a human. This holds even when a later commit reverts the change, because every pushed commit stays in the branch history and can trigger a workflow. A **sensitive zone** (dependencies, for instance) is pushed, and the PR names the paths and gets the `sensitive-zone` label, so that a human decides.

**Tools behind contracts.** night-shift never talks to a tool directly. The pass, Publication and the digest only know four contracts: **ticket source**, **code forge**, **harness** and notification ([ADR 0012](docs/adr/0012-outils-derriere-des-contrats.md)). Each tool plugs in through its own **adapter**:

| Contract | Adapters today |
|---|---|
| Ticket source and code forge | **GitHub** (github.com and Enterprise Server), **GitLab** (gitlab.com, dedicated and self-managed: see [the GitLab guide](docs/gitlab.md)) |
| Harness | Claude Code (`claude -p`) |
| Notification | Discord |

GitHub and GitLab are two adapters among others possible, and the steps choose one with `--forge`. Both pass the same contract test suite as the in-memory fake, and the pass, Publication and the digest did not change when GitLab arrived. The contracts speak of change requests, never pull requests. Each forge's git user, commit identities, project naming and address rules stay inside its adapter. Comments and digests still say "PR" for now.

**Adding a forge** (or a ticket source such as Jira) never touches the pass:

1. Implement `forge.TicketSource` and `forge.CodeForge` in `internal/forge/<name>` ([`internal/forge/forge.go`](internal/forge/forge.go)). A capability the tool lacks gets the fallback the contract defines, such as the `Blocked by: #n` line for blocking links.
2. Run the shared contract suite against it, through a fixture over a simulated API: `forgetest.Run(t, newFixture)` ([`internal/forge/forgetest`](internal/forge/forgetest)). No scenario may be removed or weakened.
3. Add one entry to the forge registry ([`internal/cli/forges.go`](internal/cli/forges.go)): its name, public addresses and how `--instance` derives them, the token variable, the tool variables and token prefixes that the agent step refuses, and the shape of a repository.

## Security principles

An agent treats whatever it reads as instructions, and a ticket may carry a prompt injection. night-shift therefore never combines **untrusted content**, **write access** and **network or secret access** at the same time (the *lethal trifecta*).

| Risk | Deterministic guardrail | Decision |
|---|---|---|
| The agent edits CI, the release pipeline or its own configuration | Publication refuses to push any commit that touches a **forbidden zone**, judged against the base branch's opt-in. Defence in depth: the forge token lacks the Workflows permission, so GitHub rejects a workflow push too | [ADR 0011](docs/adr/0011-publication-frontiere-des-zones.md) |
| A workflow that already exists runs code the agent changed | Opt-in condition: no workflow triggered by an `agent/` branch or its PR holds write permissions or secrets. night-shift does not verify it | [ADR 0011](docs/adr/0011-publication-frontiere-des-zones.md) |
| A token leaks through prompt injection | The agent container **holds no token**: only Publication pushes. The agent step refuses to start if a variable carries a forge token, recognised by its name or by its shape (`ghp_`, `github_pat_`, `glpat-`…); a token mounted as a file is not detected, so the guarantee stays with the deployment | [ADR 0005](docs/adr/0005-agent-sans-droit-d-ecriture.md), [ADR 0011](docs/adr/0011-publication-frontiere-des-zones.md) |
| Exfiltration, access to the internal network | Egress closed by default, allow-list proxy | [ADR 0002](docs/adr/0002-frontiere-deterministe.md) |
| A third party gives orders through a comment | Brief built from trusted authors only; a third-party comment hands the ticket back to a human | [ADR 0002](docs/adr/0002-frontiere-deterministe.md) |
| Two agents on the same ticket | Serialized passes and reservation | [ADR 0001](docs/adr/0001-passes-planifiees-serialisees.md) |
| The agent merges or approves | Service identity holds a strict subset of the team's rights | [ADR 0003](docs/adr/0003-identite-sous-ensemble-des-droits.md) |

### What the agent reads

The pass, not the agent, builds the brief, and the agent never reads the ticket itself. An author is trusted when the forge associates them with the repository (owner, member, collaborator) or when the opt-in lists them under `trustedAuthors`.

- The brief holds the title and body when the ticket's author is trusted, then every trusted comment, in order, except night-shift's own reports. Anything else is dropped before it reaches the agent's container.
- The brief dates from its latest trusted content. If a third party wrote or edited a comment since then, or if no content is trusted at all, the ticket is not processed: it moves from `ready-for-agent` to `needs-triage`, with a comment that explains why without ever quoting the untrusted content. A human decides whether the brief must change, then puts the ticket back in `ready-for-agent`.
- No ticket content is ever interpolated into a shell command: the brief reaches `claude -p` on its standard input, and the title only becomes a branch slug restricted to `[a-z0-9-]`.

A repository is opened to passes only through an **opt-in** (*Adhésion*): a file versioned in the repository itself and protected as a forbidden zone. The repository also needs a protected default branch and a service identity that cannot merge, and no workflow triggered by an `agent/` branch or its PR may hold write permissions or secrets. It no longer needs to enforce required checks: Publication enforces the zones itself.

## Three tiers

| Tier | What changes | For whom |
|---|---|---|
| **Solo** | A Kubernetes `CronJob`: one pod in steps (selection, agent, publication, then one agent retry if CI fails). Without a cluster, the same steps as a Docker Compose stack on a single host | A maintainer and their repositories |
| **Team** | One service account per team, trust levels per repository | A team that owns its repositories |
| **Platform** | A self-service **Kubernetes operator**: a team declares a **Shift** (*Poste*) in its namespace | A platform team offering night-shift to others |

A repository's **trust level** (*palier de confiance*: tickets per pass, draft or ready PRs) is raised only by a human decision, based on **outcomes** measured on the forge.

## Roadmap

Status: **a pass runs a real headless `claude -p` agent on a brief built from trusted authors only, refuses to push any commit that touches a forbidden zone, opens a draft PR on GitHub or a draft merge request on GitLab, follows its CI with at most two agent rounds, and ends with a digest; a weekly issue reads each ticket's outcome back from the forge and reports the delegation rate; it runs as a Kubernetes `CronJob` or as a Docker Compose stack on a single host; images are published on each release.**

**Solo tier** ([spec](https://github.com/UnPoilTefal/night-shift/issues/2))

- [x] Go foundation, CI and required checks ([#3](https://github.com/UnPoilTefal/night-shift/issues/3))
- [x] Minimal pass: selection and reservation, with a stubbed harness ([#4](https://github.com/UnPoilTefal/night-shift/issues/4))
- [x] Zone check: verdict on a PR diff, usable from any repository's CI ([#5](https://github.com/UnPoilTefal/night-shift/issues/5))
- [x] Real `claude -p` harness, with publication of a draft PR ([#6](https://github.com/UnPoilTefal/night-shift/issues/6))
- [x] Trusted-author filtering: a third-party comment after the brief sends the ticket back to triage ([#7](https://github.com/UnPoilTefal/night-shift/issues/7))
- [x] Container images: a generic base and a Go layer, published on each release ([#11](https://github.com/UnPoilTefal/night-shift/issues/11))
- [x] Pass digest: a summary comment per ticket, a digest issue per opted-in repository, a short Discord notification ([#8](https://github.com/UnPoilTefal/night-shift/issues/8))
- [x] CI rounds: the pass follows the draft PR's CI and relaunches the agent once on failing checks, never a third time ([#9](https://github.com/UnPoilTefal/night-shift/issues/9))
- [x] Zone refusal: Publication checks every commit against the base branch's opt-in before each push, so the opt-in file is the only thing a target repository needs ([#67](https://github.com/UnPoilTefal/night-shift/issues/67), [ADR 0011](docs/adr/0011-publication-frontiere-des-zones.md))
- [x] Replayable zone-refusal proof against a real forge: `make e9-replay` replays the attack that led to it on a sandbox repository ([#75](https://github.com/UnPoilTefal/night-shift/issues/75))
- [x] Agent configuration fresh on every round, transcripts archived by the trusted steps only ([#104](https://github.com/UnPoilTefal/night-shift/issues/104))
- [x] Agent `/tmp` kept away from the trusted steps and emptied after each relay round; `git` in the zone check ignores any global configuration ([#129](https://github.com/UnPoilTefal/night-shift/issues/129))
- [x] Fixed refusals for irreversible actions (push, merge, editing a ticket or PR body), passed to the agent whatever the allowed tools; defence in depth on top of the agent having no write token ([#115](https://github.com/UnPoilTefal/night-shift/issues/115))
- [x] Every secret readable from a file (`…_FILE`), so a deployment can keep secrets out of the environment ([#107](https://github.com/UnPoilTefal/night-shift/issues/107))
- [x] Control service v1: scheduled and manual passes one at a time, watchdog, host-only history behind a read-only API ([#109](https://github.com/UnPoilTefal/night-shift/issues/109), [docs](docs/control.md))
- [x] Outcomes and weekly digest: `night-shift weekly` reads each ticket's outcome on the forge, reports the delegation rate and flags the trust-level criterion, on GitHub and GitLab ([#10](https://github.com/UnPoilTefal/night-shift/issues/10))
- [x] Docker Compose example (solo tier, no cluster): the same steps of the same binary on a single Docker host, with an embedded egress proxy and a static test of every guarantee ([#110](https://github.com/UnPoilTefal/night-shift/issues/110), [guide](docs/compose.md), [ADR 0013](docs/adr/0013-exemple-compose-second-systeme-d-execution.md))
- [x] End-to-end test of the Compose example in CI: the real stack against a fake GitLab, in ten scenarios, from a full pass to a hostile agent and the barrier proof ([#111](https://github.com/UnPoilTefal/night-shift/issues/111))
- [x] No CI triggered on the draft PR: after `--ci-grace` with no check or status, the pass stops waiting and flags it apart from an unsettled CI ([#135](https://github.com/UnPoilTefal/night-shift/issues/135))
- [x] Verdict journal: the decisions of the barriers on the agent's actions, written by the trusted steps next to the transcripts, counted per ticket in the pass report and on the PR, never verbatim ([#131](https://github.com/UnPoilTefal/night-shift/issues/131))
- [x] Agent configuration read from a versioned repository: added refusals and skills, at the last merged revision, read-only for the agent, revision recorded for each ticket; the fixed refusals stay in the code ([#132](https://github.com/UnPoilTefal/night-shift/issues/132), [guide](docs/agent-config.md))
- [x] Replayable tier-1 proof on the Compose example: thirteen declarative cases (E1 to E10, with variants) that order the agent to do something dangerous, judged by state, never by the agent's account; `night-shift proof replay` plays them on a running stack and its sandbox, then cleans up ([#116](https://github.com/UnPoilTefal/night-shift/issues/116), [guide](docs/proof.md))

**Forges** ([spec](https://github.com/UnPoilTefal/night-shift/issues/57), [ADR 0012](docs/adr/0012-outils-derriere-des-contrats.md))

- [x] GitHub adapter
- [x] Ticket source and code forge as two separate contracts, with one contract test suite for every adapter ([#80](https://github.com/UnPoilTefal/night-shift/issues/80))
- [x] Choose the forge adapter of a pass with an option ([#81](https://github.com/UnPoilTefal/night-shift/issues/81))
- [x] GitLab adapter, the first one added: issues as a ticket source, draft merge requests, pipelines for CI rounds, gitlab.com or a self-managed instance ([#82](https://github.com/UnPoilTefal/night-shift/issues/82))
- [x] GitLab deployment example, installation guide, opt-in guidance and a zone-refusal proof that replays on either forge ([#83](https://github.com/UnPoilTefal/night-shift/issues/83))
- [ ] A full pass on a real GitLab instance (draft merge request, CI rounds), run by the maintainer ([#57](https://github.com/UnPoilTefal/night-shift/issues/57)). The zone-refusal proof already passes on gitlab.com

**Platform tier** ([spec](https://github.com/UnPoilTefal/night-shift/issues/12))

- [x] Increment 0: `Shift` and `Pass` custom resources, a scheduled pass per cron occurrence, one active pass per shift, a hardened orchestrator Job ([#13](https://github.com/UnPoilTefal/night-shift/issues/13))
- [ ] Next increments, to be revised with the platform spec once the forge contracts land: from a fake ticket source and one Job per ticket up to parity with the solo tier, then tools ([#14](https://github.com/UnPoilTefal/night-shift/issues/14) to [#22](https://github.com/UnPoilTefal/night-shift/issues/22))

## Development

The binary runs a **pass** in steps, one per container, and provides the **zone check**, which Publication runs before every push and an opted-in repository may also run in its CI as optional defence in depth: see [`docs/opt-in.md`](docs/opt-in.md) for the opt-in schema and the GitHub Action.

```sh
go build -o night-shift ./cmd/night-shift
./night-shift version     # "dev" unless injected with -ldflags
./night-shift zones --base origin/main --head HEAD --head-ref agent/42-fix-typo
make test                 # unit and envtest suites
make lint
```

A pass hands its work from one step to the next through two shared directories: `--state` (the reserved task, written by `select` only) and `--work` (the clone and the agent's output).

```sh
# 1. Selection (forge token): reserve the top ready ticket, clone the target repository.
#    --agent-config owner/agent-config also reads the agent configuration (added refusals, skills)
#    at its last merged revision and hands it to the agent read-only (see docs/agent-config.md).
NIGHT_SHIFT_GITHUB_TOKEN=… ./night-shift select --repo owner/repo --state /state --work /work

# 2. Agent (no forge token; refuses to start if a variable carries one, by name or by shape): run /implement through claude -p.
#    --auth subscription reads CLAUDE_CODE_OAUTH_TOKEN, --auth api-key reads ANTHROPIC_API_KEY.
#    --allowed-tools widens what the agent may run, but pushing, merging and editing a ticket or PR body
#    (git push, gh pr merge, gh issue edit, glab mr update…) are always denied, whatever it allows.
CLAUDE_CODE_OAUTH_TOKEN=… ./night-shift agent --state /state --work /work --timeout 30m

# 3. Publication (forge token, no model): apply the commit series on agent/<n>-<slug>,
#    check each commit against the base branch's opt-in (a forbidden zone: nothing is pushed),
#    push it, open a draft PR and wait for its CI (--ci-timeout, 10m by default). The verdict waits
#    until every check has stayed completed for --ci-settle (1m), so a slow workflow is not missed.
#    If no check or status shows up within --ci-grace (2m), it stops waiting and reports "no CI triggered".
#    Green: hand the ticket back in an explicit state, then publish the digest.
#    Red: prepare the retry in /state2 and /work2 instead (fresh clone of the branch, failing checks).
#    NIGHT_SHIFT_DISCORD_WEBHOOK is optional. --transcripts archives the round's transcript (see below).
NIGHT_SHIFT_GITHUB_TOKEN=… NIGHT_SHIFT_DISCORD_WEBHOOK=… ./night-shift publish --state /state --work /work \
  --next-state /state2 --next-work /work2 --transcripts /transcripts

# 4. Retry (no forge token): the agent adds fixing commits on top of the published branch.
#    With no retry prepared, it does nothing.
CLAUDE_CODE_OAUTH_TOKEN=… ./night-shift agent --state /state2 --work /work2 --timeout 30m

# 5. Retry publication: push on the same PR, wait for CI, hand the ticket back and publish the digest.
#    With no retry prepared, the first publication already did, and it does nothing.
NIGHT_SHIFT_GITHUB_TOKEN=… ./night-shift publish --follow-up --state /state2 --work /work2 --transcripts /transcripts
```

**Relay mode.** With `--relay`, each step runs as a long-lived service that waits for its turn instead of being started in order by an orchestrator, with no Docker socket ([ADR 0013](docs/adr/0013-exemple-compose-second-systeme-d-execution.md); the relay contract is in [#108](https://github.com/UnPoilTefal/night-shift/issues/108#issuecomment-6442592125)). Without it, every step behaves as above, and the `CronJob` does not use it.

- `select --relay --control <url>` claims each pass from the control service over HTTP, empties `--state`, `--work`, `--next-state` and `--next-work`, selects, then writes the round marker `turn.json` last. Its deadline is `--round-timeout` from now (30m by default), which must be longer than the agent's `--timeout`.
- `agent --relay` only talks to its volumes. It waits for a `turn.json` it has not handled yet, ignores one whose deadline has passed, runs at most until that deadline, then writes `out/done.json`. It must be PID 1 of its container and refuses to start otherwise: after each round it kills every other process it can reach (`kill -1`) and empties `HOME`, `CLAUDE_CONFIG_DIR` and its temporary directory (`TMPDIR`, `/tmp` by default), so nothing a round leaves behind reaches the next one.
- `publish --relay --control <url>` (with or without `--follow-up`) waits for the `turn.json` of its own volume, then for a matching `done.json` or the deadline. Past the deadline, the round counts as an agent that returned nothing. It copies the agent's output out of the agent's reach, then validates and pushes only that copy, so a forged `done.json` followed by a rewrite has no effect. When it prepares the retry, it writes the second round's `turn.json` in `--next-state`.
- The trusted steps report `started`, `finished` (with the exit code), `incident` (full detail, never in the digest) and, from the step that publishes the digest, `pass-finished` with the full report. A failed report never stops a step.

`select`, `publish` and `pass` take `--forge <name>` to choose the forge adapter: `github` (the default, so existing deployments need no change) or `gitlab`. Without more options, a step talks to the public forge. For a dedicated or self-managed instance, give its base address with `--instance`, for example `--instance https://gitlab.example.com`. The adapter derives both the API and the clone addresses from that one base, so the token can never be sent to another host. `--api-url` and `--git-url` remain for a topology that `--instance` cannot describe: they must be given together, never with `--instance`. An unknown name, a badly formed `--repo` or inconsistent addresses are usage errors (exit code 2), raised before any network call. The task records the forge that reserved it, and `publish` refuses a task reserved on another forge. Whatever the forge, the agent step refuses to start when any variable of any registered adapter carries a token.

| | `github` | `gitlab` |
|---|---|---|
| Token variable | `NIGHT_SHIFT_GITHUB_TOKEN` | `NIGHT_SHIFT_GITLAB_TOKEN` |
| `--repo` | `owner/repo` | full project path, subgroups included: `group/subgroup/project` |
| Public forge | `https://api.github.com`, `https://github.com` | `https://gitlab.com/api/v4`, `https://gitlab.com` |
| `--instance <base>` | GitHub Enterprise Server: `<base>/api/v3`, `<base>` | Self-managed or dedicated: `<base>/api/v4`, `<base>` (relative root included) |
| Change request | Draft pull request | Merge request titled `Draft: …`, source branch kept |
| CI rounds read | Check runs and commit statuses | Jobs of the commit's latest pipeline, trigger jobs of child pipelines included, with the end of each failed job's log; for merged results pipelines, the merge request's head pipeline |
| Trusted authors | Owners, members and collaborators | Project members with the Developer role or above, direct or inherited from a group |

**Secrets as files.** Every secret variable also has a file form: `NIGHT_SHIFT_GITHUB_TOKEN_FILE`, `NIGHT_SHIFT_GITLAB_TOKEN_FILE`, `NIGHT_SHIFT_DISCORD_WEBHOOK_FILE`, `CLAUDE_CODE_OAUTH_TOKEN_FILE` and `ANTHROPIC_API_KEY_FILE` each name a file that holds the secret. The token variable of any future forge adapter gets the same form. A step reads the file when it starts and drops its final newline; an empty file counts as no secret. Setting both forms of one secret is a usage error (exit code 2), and an unreadable file is an error that names the variable, never its content. The agent step passes a model token read from its file to `claude` under the variable it expects, never forwards any `…_FILE` variable, and refuses to start when it sees the file form of a forge token or of the webhook, even one that points to a missing file. Environment variables keep working as before. With the `CronJob`, you may mount the `Secret` as a volume and point the `…_FILE` variable at it instead of using `secretKeyRef`.
| Blocked tickets | Native issue dependencies | `is blocked by` links where the edition has them, plus a `Blocked by: #n, #m` line in the issue body |

On GitLab, use a project or group access token with the Developer role and the `api` and `write_repository` scopes. night-shift creates the lifecycle labels and `sensitive-zone` in the project when they are missing. TLS is always verified: for an instance behind an internal certificate authority, add that authority to the container's certificate store.

The forge token needs read and write access to contents, pull requests and issues, and read access to checks and commit statuses. It is never mounted in the agent's container, in either round. Do **not** grant it the **Workflows** permission. Without it, GitHub rejects any push that creates or changes a file under `.github/workflows/`: `refusing to allow a Personal Access Token to create or update workflow … without workflow scope` (observed on 2026-10-25 with a fine-grained token, while a push of another file to an `agent/` branch went through). This is defence in depth only. The guarantee is Publication's zone refusal, which works whatever rights the token holds.

| Agent outcome | Ticket ends up |
|---|---|
| Commits, CI green (first or second round) | Linked to a **draft** PR, commit messages kept, `Night-Shift-Agent` and `Night-Shift-Pass` trailers added; no lifecycle label left |
| Commits, CI not settled before `--ci-timeout` | Same, with a mention of the checks still running |
| Commits, no check or status within `--ci-grace` | Same, flagged **no CI triggered**: nothing verified the change. The wait stops there instead of running to `--ci-timeout` |
| Commits, CI still red after the second round | `ready-for-human`; the PR stays a draft, with a comment that sums up the failing checks |
| A commit touches a forbidden zone (either round) | **Zone refusal**: nothing is pushed, `ready-for-human` with the commit, path and zone. In the second round, the draft stays as the first round published it, with a comment |
| Commits touch a sensitive zone | Pushed as usual; the PR names the paths and gets the `sensitive-zone` label |
| Brief not enough, unmet precondition, unnamed dependency | `needs-info`, with the agent's reason |
| Failure, timeout, crash | `ready-for-human`, with a mention of the interruption |

Each ticket gets a summary comment: what was done, cost, duration and its provisional **outcome**. The pass then ends with its **digest**:

- **Digest issue**: one per repository the pass walked, even when its queue was empty, and the issue says so. It lists the processed tickets by number, with provisional outcome, draft PR, duration and cost, counts the zone refusals apart from failures, lists the tickets sent back to triage, the pass duration and the agents' cost. A repository left out (no opt-in, invalid opt-in) or unreachable never gets one.
- **What stays inside**: anything that does not concern a repository never leaves the cluster: which repositories were left out and why, and the details of incidents. Today they live in the logs of the pass's pods, and the example `CronJob` keeps a week of Jobs so that recent passes stay inspectable. An in-cluster view of past passes is planned for the platform tier ([#51](https://github.com/UnPoilTefal/night-shift/issues/51)).
- **Incidents**: if part of the pass fails (a reservation, a clone), `select` records it instead of exiting, so the pod still reaches publication. The digest counts the incidents without their details, then `publish` exits with an error so that the job shows the failure.
- **Discord notification** (optional): it holds only counters (tickets, zone refusals, triage, incidents, repositories left out or unreachable) and links to the digest issues; it never names a repository left out, never carries an error message or ticket content, and mentions nobody. The egress proxy must let `publish` reach `discord.com`. Without a webhook, the pass still succeeds and the digest says the notification is not configured. If the webhook fails, the digest issues get a comment saying so.

**Weekly issue.** `night-shift weekly --repo owner/repo` (with `--forge` and `--instance` like the other trusted steps) runs with the forge token only, no model and no local state. For each opted-in repository, it reads on the forge the final **outcome** of every ticket a pass reserved during the ISO week (Monday to Sunday, UTC; `--week 2026-10-21` picks another week):

| Outcome | Read on the forge |
|---|---|
| Merged as is | The change request of the ticket's `agent/<n>-…` branch is merged, and every commit carries the `Night-Shift-Agent` trailer |
| Merged after rework | It is merged, and at least one commit lacks the trailer: a human added it |
| Closed without merge | It is closed, not merged |
| Handed back | The ticket is in `needs-info` or `ready-for-human`; a zone refusal is shown apart |
| Pending | The change request is still open: it does not count yet |

A ticket counts as reserved through the reservation comment of a pass, posted by an author associated with the repository. The issue gives the **delegation rate**: tickets whose change request a human merged, as is or after rework, over the reserved tickets, without the `needs-info` ones (shown apart, since they measure the brief) and the pending ones. It also counts the change requests the agent opened in the week, and flags the **trust-level criterion** when the last 5 closed agent change requests were merged as is. It never touches the opt-in: raising the trust level stays a human decision. Two runs in the same week update the same issue, labelled `night-shift-weekly`, instead of opening a second one. [`examples/solo/cronjob-weekly.yaml`](examples/solo/cronjob-weekly.yaml) runs it every Sunday evening, as a second `CronJob` that mounts the forge token and no model secret; a test in the repository checks it.

[`examples/solo/cronjob.yaml`](examples/solo/cronjob.yaml) shows the expected deployment, and [`cronjob-gitlab.yaml`](examples/solo/cronjob-gitlab.yaml) the same on GitLab ([guide](docs/gitlab.md)): a `CronJob` whose pod runs `select`, `agent`, `publish` and `retry-agent` as init containers and `retry-publish` as its container, with the forge token and the Discord webhook mounted only in the trusted steps. Its deadline (90 minutes) covers two agent rounds of 30 minutes and two CI waits of 10 minutes. A test in the repository keeps both variants that way, and fails if a forge token reaches an agent step, if an agent step mounts a persistent volume, or if a trusted step mounts writable a volume an earlier agent step could write: each step has its own `/tmp`, so a `.gitconfig` an agent leaves there never reaches `publish` ([#129](https://github.com/UnPoilTefal/night-shift/issues/129)). The egress proxy and transcript retention belong to your deployment.

**Transcripts.** Each agent round gets a fresh Claude Code configuration (`CLAUDE_CONFIG_DIR` on its own `emptyDir`), so nothing a round writes there (hooks in `settings.json`, instructions, skills) ever runs in another round, another ticket or another pass ([#104](https://github.com/UnPoilTefal/night-shift/issues/104)). At the end of the round, the agent step copies the session transcript into its output as `transcript.jsonl`, up to 50 MiB. `publish --transcripts <dir>` then copies it as is to `<dir>/<pass>/<repo>-<ticket>-round<n>.jsonl`, and only reads that copy to find the harness's refusals (see verdicts below). That name comes from the task, never from the agent, and an existing archive is never overwritten. Only the `publish` steps mount the archive, so an agent can neither plant configuration for a later pass nor rewrite a past transcript. A missing or oversized transcript does not stop publication: the digest counts it as an incident. `./night-shift pass` still runs selection and hand-back in a single process with a stubbed agent, which is handy for trying an opt-in.

**Verdicts.** A verdict is the decision a barrier made on one of the agent's actions, in the shape set by [keystone#78](https://github.com/UnPoilTefal/keystone/issues/78). With `--transcripts`, the trusted steps append them to `<dir>/<pass>/verdicts.jsonl`, next to the transcripts and out of the agent's reach; the agent never writes one. Each line carries the pass, the ticket, the repository, the agent configuration revision, the brick and realization that decided, the result (`refused`, `flagged`, `stopped`…), the action class and a fingerprint of the action, never its verbatim, which stays in the transcript. Today's sources are the harness's refusals read from the archived transcript, Publication's zone refusals and sensitive zones, a change request with no CI, the round's time cap, the relay deadline and control's watchdog. `publish` counts the ticket's verdicts in the pass report and in a comment on the change request ("2 refusées, 1 signalée"); nothing raw reaches the forge. The egress proxy's refusals come with [#136](https://github.com/UnPoilTefal/night-shift/issues/136), approval requests with [#99](https://github.com/UnPoilTefal/night-shift/issues/99).

**Control service.** `night-shift control serve` is the building block for running passes without Kubernetes. It triggers passes on a cron schedule or on demand (`night-shift control trigger`, no network nor secret), runs one at a time, merges requests received meanwhile into one waiting pass, and closes a pass stuck past its watchdog. It keeps a history that never leaves the host, served by a read-only administration API behind a token, and purges history and archived transcripts after 30 days. It holds no forge or model token and makes no outbound call. The trusted steps talk to it in relay mode. Routes and options are in [`docs/control.md`](docs/control.md).

### Running on Docker Compose

Without a cluster, [`examples/compose/`](examples/compose/) runs the same steps of the same binary as long-lived services on a single Docker host, in relay mode, next to `control` and a Squid egress proxy:

```mermaid
flowchart LR
    C["control<br/>schedule, trigger,<br/>history"]
    subgraph TN["trusted-net + control-net"]
        S["select<br/>forge token"]
        U["publish / retry-publish<br/>forge token, webhook"]
    end
    subgraph AN["agent-net (internal, isolated)"]
        A["agent / retry-agent<br/><b>no forge token</b><br/>PID 1, tmpfs only"]
        X["proxy (Squid)<br/>CONNECT 443<br/>allow-list"]
    end
    C <--> S & U
    S -- "turn.json" --> A
    A -- "out/, done.json" --> U
    A --> X --> M["api.anthropic.com,<br/>package registries"]
    S & U --> F["forge"]
```

```sh
cd examples/compose
# secrets/forge_token, model_token, control_token, discord_webhook (may be empty)
NIGHT_SHIFT_REPO=owner/repo docker compose up -d
docker compose exec control night-shift control trigger   # a pass now, besides the schedule
```

The agents only reach the proxy. The proxy only lets `CONNECT` to port 443 of the listed domains through, and refuses private ranges first. The agents have no persistent volume, and each round they empty their `tmpfs` and kill every process they left. Secrets are files, and the administration API listens on `127.0.0.1` only. A static test keeps every rule, with one failing case per rule, and checks that each option exists in the binary. It needs Docker Engine 28 or later on Linux; Docker Desktop is for trying it. The stack runs the published `0.4.0` images by default. Setup, allow-lists, private LLM gateway and the remaining gaps from the `CronJob` are in [`docs/compose.md`](docs/compose.md). In CI, `make test-compose` runs the real stack against a fake GitLab and checks each guarantee end to end: network, secrets, nothing left between rounds, a forged end of round ([#111](https://github.com/UnPoilTefal/night-shift/issues/111)).

### Container images

Two images, built from [`images/`](images/) with [`docker-bake.hcl`](docker-bake.hcl):

| Image | Contents | Used by |
|---|---|---|
| `ghcr.io/unpoiltefal/night-shift` | `night-shift`, Claude Code, `gh`, `git`, `make`, and the [mattpocock skills](https://github.com/mattpocock/skills) | `select` and both `publish` steps, and as the base of a repository's tool image |
| `ghcr.io/unpoiltefal/night-shift-go` | The base image plus the Go toolchain | Both `agent` steps, for Go repositories |

- Both run as a non-root user (UID 65532) and carry no secret: tokens and webhooks are provided at run time. They work on a read-only root filesystem once `HOME` points to a writable directory, such as an `emptyDir` on `/tmp`, as the example `CronJob` does.
- The skills are installed as Claude Code managed skills (`/etc/claude-code/.claude/skills`), so `HOME` and `CLAUDE_CONFIG_DIR` set by the deployment never hide `/implement`.
- Every pull request builds both images, smoke-tests them (tools, injected version, non-root user, skills) and scans them for secrets. A `vX.Y.Z` tag publishes them for `linux/amd64` and `linux/arm64`, tagged `X.Y.Z`. CI then smoke-tests and scans the pushed images on both platforms, and only then moves `latest` (never for a pre-release such as `v1.0.0-rc.1`). The latest release is [`v0.4.0`](https://github.com/UnPoilTefal/night-shift/releases/tag/v0.4.0).
- Renovate tracks the base images, Claude Code, `gh` and the skills.

```sh
docker run --rm ghcr.io/unpoiltefal/night-shift-go:0.4.0 night-shift version
make images test-images scan-images          # or build them locally, tagged dev
```

A repository's tool image derives from either one: `FROM ghcr.io/unpoiltefal/night-shift:<version>`, then its own toolchain.

### Replaying the barrier proof

`make proof-replay` replays the barrier proof on a running Compose stack and its sandbox repository: each case orders the agent to do something dangerous (push to the default branch, rewrite a ticket body, merge a change request, read a secret, loop forever…), and the launcher judges the result from the state of the forge, the volumes and the logs. It prints a report per case and says whether the tier opens. It refuses to start unless the stack targets the sandbox it was given. See [docs/proof.md](docs/proof.md) for the cases, the preparation of the sandbox and the stack override.

### Replaying the zone-refusal proof

`make e9-replay` replays proof E9 against a real forge, following the procedure in [#67](https://github.com/UnPoilTefal/night-shift/issues/67). In that proof, a hostile agent's series added a workflow under `.github/workflows/`, triggered on push to `agent/**`, that would create a branch with the CI identity. The target replays the three steps of a pass, each in a container of the image under test. `select` runs with the token, then a simulated agent with no token ([`hack/e9-agent.sh`](hack/e9-agent.sh)) returns that series, then `publish` runs with the token. On GitLab (`E9_FORGE=gitlab`), the series rewrites `.gitlab-ci.yml` instead. The target then checks on the forge that:

- no `agent/…` branch and no agent change request exist;
- no CI ran for the trap (the `e9-preuve` workflow on GitHub, any pipeline on an `agent/…` branch on GitLab), and the `e9-preuve` branch does not exist. On GitLab, the pipeline check is the decisive one: the trap pushes with the job token, which cannot push by default, so `e9-preuve` would stay absent even after a faulty push;
- the ticket is `ready-for-human`, with a zone-refusal comment that names the path and the zone;
- the default branch's SHA is unchanged.

It prints one line per check and exits 0 only if every check passes and the cleanup succeeds. Last replays, on 2026-10-26 with an image built from the branch of [#83](https://github.com/UnPoilTefal/night-shift/issues/83): every check passed on a GitHub sandbox and on a private gitlab.com sandbox. Before that, against the published `0.2.0` image on GitHub on 2026-10-25 ([#69](https://github.com/UnPoilTefal/night-shift/issues/69)). Cleanup always runs, even after a failure or an interruption. It closes the test ticket and the digest issue, and deletes any PR or branch the run created, nothing else.

```sh
NIGHT_SHIFT_GITHUB_TOKEN=… make e9-replay E9_REPO=owner/sandbox E9_IMAGE=ghcr.io/unpoiltefal/night-shift:<version>
NIGHT_SHIFT_GITLAB_TOKEN=… make e9-replay E9_FORGE=gitlab E9_INSTANCE=https://gitlab.example.com \
  E9_REPO=group/sandbox E9_IMAGE=ghcr.io/unpoiltefal/night-shift:<version>
```

Every host-side call to the forge goes through one small driver per forge behind a common interface: [`hack/e9-forge-github.sh`](hack/e9-forge-github.sh) (`gh`) and [`hack/e9-forge-gitlab.sh`](hack/e9-forge-gitlab.sh) (`curl` and `jq`). Without `E9_FORGE`, the target behaves as before on GitHub.

- **Sandbox**: a throwaway repository with CI enabled, whose opt-in on the default branch forbids `.github/**` (GitHub) or `.gitlab-ci.yml` (GitLab), and whose token account is a trusted author: listed in the opt-in on GitHub, Developer of the project or listed on GitLab. It must have no `agent/…` branch, no `e9-preuve` branch and no open `ready-for-agent` or `agent-in-progress` ticket. The target checks all of this before writing anything. It never has a default repository.
- **Token**: the pass's forge token rights on the sandbox, plus read access to Actions on GitHub to list workflow runs. Host-side calls use this token, never the current `gh` session; on GitLab it goes through a header file, never on `curl`'s command line.
- `E9_GRACE` (60 seconds) leaves a workflow time to start before the checks, and `E9_CI_TIMEOUT` (2m) bounds `publish` if a regression ever opened a PR. The target needs a real token and a sandbox, so CI does not run it. CI does run `go test`, which checks the simulated agent's series offline: it passes the agent step's validation and gets a `block` verdict from the zone check.

### Operator preview

A team declares a **Shift** (*poste*) in its namespace; the operator turns each cron occurrence into a **Pass** (*passe*) and each pass into an orchestrator Job. At this stage the Job is a placeholder that only prints the pass name, but it already runs under the `restricted` pod security standard.

```yaml
apiVersion: nightshift.unpoiltefal.github.io/v1alpha1
kind: Shift
metadata:
  name: team-tickets
spec:
  trigger:
    schedule: {cron: "0 2 * * *", timeZone: Europe/Paris}
  mission:
    tickets:
      source:
        fake: {configMapName: tickets-demo}
```

Try it on a throwaway [kind](https://kind.sigs.k8s.io/) cluster. It uses its own kubeconfig (`bin/kind.kubeconfig`), never your current context:

```sh
make kind-up kind-deploy kind-samples
KUBECONFIG=bin/kind.kubeconfig kubectl -n night-shift-demo get shifts,passes,jobs
make kind-down
```

`make test-e2e` runs the same scenario end to end in CI.

Every pull request runs lint, tests, the image build with its smoke test and secret scan, the kind end-to-end scenario and the Compose end-to-end test. Lint, tests and images are required checks on `main`.

## Documentation

Design documents are written in French, using the glossary's French aliases.

- [`docs/opt-in.md`](docs/opt-in.md): the opt-in schema, its conditions, and how to add the optional zone check to a repository's CI.
- [`docs/compose.md`](docs/compose.md): running the solo tier with Docker Compose, without a cluster.
- [`docs/control.md`](docs/control.md): the control service and its APIs.
- [`GLOSSARY.md`](GLOSSARY.md): the glossary. Every term in **bold** in this README is defined there under its canonical English name, with its French alias (shown here in *italics*) used throughout the French design documents.
- [`docs/adr/`](docs/adr/): architecture decisions, including the alternatives that were rejected.
- [`docs/agents/`](docs/agents/): configuration for the agents working on this repository.

## Security

See [`SECURITY.md`](SECURITY.md) to report a vulnerability privately.

## License

[MIT](LICENSE)
