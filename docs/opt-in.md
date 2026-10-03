# Opt-in and zone check

A repository opens itself to night-shift passes with an **opt-in** (*adhésion*): a file versioned in the repository at `.night-shift/opt-in.yaml`. A repository without a valid opt-in is never touched.

## Schema

```yaml
version: 1                      # required, only 1 is supported

trustLevel:                     # required, raised only by a human decision
  ticketsPerPass: 1             # >= 1
  pullRequests: draft           # draft | ready

toolImage: ghcr.io/example/tools-go:1   # optional, image providing the repo's build and test toolchain

trustedAuthors:                 # optional, in addition to the forge's own association (owners, members)
  - alice

forbiddenZones:                 # paths an agent PR must never modify: the zone check blocks it
  - ".github/**"
  - "CLAUDE.md"

sensitiveZones:                 # paths an agent PR may modify, but a human decides
  - path: go.mod
    kind: go-dependencies       # flags only a new direct requirement or a new/changed replace
  - path: "deploy/**"           # kind defaults to generic: any change is flagged
```

Rules:

- **Strict parsing.** An unknown field, a missing value or an invalid pattern makes the opt-in invalid. night-shift never guesses a perimeter.
- **Patterns** are relative to the repository root and support `**` (see [doublestar](https://github.com/bmatcuk/doublestar)).
- **`.night-shift/**` is always a forbidden zone**, even when not listed: an agent can neither widen its own perimeter nor rewrite the instructions for the next pass.
- **`go-dependencies`** zones are analysed per `go.mod`. A version bump or a removed dependency passes. A new direct requirement (including an indirect one becoming direct) or a new or changed `replace` directive is flagged. Other files in the zone (for instance `go.sum`) are not flagged.

## Zone check

The zone check compares the diff of an agent PR (from the merge-base to the head) with the opt-in of the **base branch**, never the one in the PR, so a PR cannot loosen the rules that judge it.

| Verdict | Meaning | Exit code |
|---|---|---|
| `pass` | No zone touched | 0 |
| `flag` | A sensitive zone is touched: a human decides | 0, with a warning |
| `block` | A forbidden zone is touched | 1 |
| *(error)* | Missing or invalid opt-in on the base | 1, with an explicit message |

Only agent PRs, whose branch starts with `agent/`, are checked. Other PRs pass, so the check can be a required check without getting in the way of humans.

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

Then make **Zone check** a required status check on the default branch, and keep `.github/**` in `forbiddenZones`. A repository that cannot enforce required checks cannot opt in (ADR 0002).

**Do not use `pull_request`**: with that trigger GitHub runs the workflow file from the PR itself, so an agent PR could replace the check with one that always passes. Never add a step that builds or runs the PR's code to this workflow either: under `pull_request_target`, that code would run with the base branch's privileges.

### From the command line

```sh
night-shift zones --base origin/main --head HEAD --head-ref agent/42-fix-typo
```
