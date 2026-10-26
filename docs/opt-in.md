# Opt-in and zone check

A repository opens itself to night-shift passes with an **opt-in** (*adhésion*): a file versioned in the repository at `.night-shift/opt-in.yaml`. A repository without a valid opt-in is never touched.

## Schema

```yaml
version: 1                      # required, only 1 is supported

trustLevel:                     # required, raised only by a human decision
  ticketsPerPass: 1             # >= 1
  pullRequests: draft           # draft | ready

toolImage: ghcr.io/example/tools-go:1   # optional, image providing the repo's build and test toolchain

trustedAuthors:                 # optional, in addition to the forge's own association
                                # (GitHub: owners, members, collaborators; GitLab: Developer members and above)
  - alice

forbiddenZones:                 # paths an agent commit must never modify: Publication refuses to push it
  - ".github/**"
  - "CLAUDE.md"

sensitiveZones:                 # paths an agent change request may modify, but a human decides
  - path: go.mod
    kind: go-dependencies       # flags only a new direct requirement or a new/changed replace
  - path: "deploy/**"           # kind defaults to generic: any change is flagged
```

Rules:

- **Strict parsing.** An unknown field, a missing value or an invalid pattern makes the opt-in invalid. night-shift never guesses a perimeter.
- **Patterns** are relative to the repository root and support `**` (see [doublestar](https://github.com/bmatcuk/doublestar)).
- **`.night-shift/**` is always a forbidden zone**, even when not listed: an agent can neither widen its own perimeter nor rewrite the instructions for the next pass.
- **Symbolic links** are judged by their path: a link placed inside a forbidden zone is refused, and a link outside every zone that points into one is not, since it changes no file of the zone.
- **`go-dependencies`** zones are analysed per `go.mod`. A version bump or a removed dependency passes. A new direct requirement (including an indirect one becoming direct) or a new or changed `replace` directive is flagged. Other files in the zone (for instance `go.sum`) are not flagged.

## Conditions

Besides a valid opt-in, a repository needs:

- a protected default branch;
- a service identity that cannot merge (ADR 0003);
- no workflow triggered by a push to an `agent/` branch or by an agent PR that holds write permissions or secrets. Such a workflow runs code the agent may have changed outside the forbidden zones (a `Makefile`, tests, scripts). night-shift does not verify this condition: it is an accepted risk (ADR 0011).

It no longer needs to enforce required checks.

## Zone refusal

Publication enforces the zones itself, before every push, in both rounds (ADR 0011):

- It judges **each commit** of the agent's series, not only the net diff: a commit that touches a forbidden zone is refused even if a later commit reverts it, because every pushed commit stays in the branch history and can trigger a workflow.
- It reads the opt-in at the base commit of the **first round**, never in the series nor at the head of the PR. A missing or invalid opt-in at that commit is a refusal too.
- A forbidden zone touched: nothing is pushed and no PR is opened. The ticket goes to `ready-for-human` with the commit, path and zone, and the digest counts the refusal apart from failures. In the retry round, the PR stays a draft, as the first round published it, with a comment.
- A sensitive zone touched: the series is pushed, the PR names the paths and gets the `sensitive-zone` label, and a human decides.

## Zone check

The same verdict is available as a check that a repository may run in its CI, as **optional defence in depth**. It compares the diff of an agent PR (from the merge-base to the head) with the opt-in of the **base branch**, never the one in the PR, so a PR cannot loosen the rules that judge it.

| Verdict | Meaning | Exit code |
|---|---|---|
| `pass` | No zone touched | 0 |
| `flag` | A sensitive zone is touched: a human decides | 0, with a warning |
| `block` | A forbidden zone is touched | 1 |
| *(error)* | Missing or invalid opt-in on the base | 1, with an explicit message |

Only agent PRs, whose branch starts with `agent/`, are checked. Other PRs pass, so the check can be a required check without getting in the way of humans. Unlike Publication, it judges the net diff of the PR.

### In GitHub Actions

```yaml
name: night-shift

on:
  # pull_request_target runs this workflow as it is on the base branch: an agent
  # PR cannot rewrite the job that judges it. It is safe here because the check
  # never runs code from the PR, it only reads git objects.
  pull_request_target:

permissions:
  contents: read

jobs:
  zones:
    name: Zone check
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7     # checks out the base branch
        with:
          fetch-depth: 0              # the check needs the base and the merge-base
      - uses: UnPoilTefal/night-shift/actions/zone-check@<commit-sha>
```

You may then make **Zone check** a required status check on the default branch; it is no longer an opt-in condition (ADR 0011). Keep `.github/**` in `forbiddenZones` either way: a workflow added by an agent would run as soon as its branch is pushed, before any check, and only Publication's refusal stops it.

**Do not use `pull_request`**: with that trigger GitHub runs the workflow file from the PR itself, so an agent PR could replace the check with one that always passes. Never add a step that builds or runs the PR's code to this workflow either: under `pull_request_target`, that code would run with the base branch's privileges.

### On GitLab

No GitLab CI equivalent of this check is needed, and none is provided. The guarantee is Publication's zone refusal, which runs before every push whatever the forge ([ADR 0011](adr/0011-publication-frontiere-des-zones.md)). A check in the target's CI could only run after the push, once a pipeline defined by the agent would already have started.

What matters on GitLab is the list of forbidden zones. A pipeline starts as soon as an `agent/…` branch is pushed, from the `.gitlab-ci.yml` **of that branch**. Forbid it, and every file it includes from the repository:

```yaml
forbiddenZones:
  - ".gitlab-ci.yml"
  - ".gitlab/**"        # CI templates included with include: local, among others
  - "ci/**"             # adapt to wherever your local includes live
```

An `include: local:` file left out of the forbidden zones would let the agent change the pipeline without touching `.gitlab-ci.yml`. See [Running night-shift on GitLab](gitlab.md) for the token, branch protection and protected variables.

### From the command line

```sh
night-shift zones --base origin/main --head HEAD --head-ref agent/42-fix-typo
```
