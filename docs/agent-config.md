# Agent configuration

The **agent configuration** (*configuration de l'agent*) is the versioned set of what guards the agent and what it reads: refusals added on top of the fixed ones, and skills. It lives in its own repository, apart from the images. The image carries the tools; the configuration is data. Promoting a lesson learned never requires rebuilding an image.

## How a pass reads it

1. `select --agent-config <repo>` clones that repository, on the same forge and with the same token as the pass, at the head of its default branch: its **last merged revision**. `select` is a trusted step: the agent never reads the repository itself. The forge token only needs read access to that repository.
2. `select` validates the configuration and copies only `agent.yaml` and `skills/` into the task volume (`/state/agent-config`). That volume is mounted read-only in the agent's container. The clone, which saw the token, never leaves the step's temporary directory.
3. The task records the repository and the revision. When a retry round is prepared, the configuration is copied with it, so both rounds of a ticket run on the same revision.
4. The agent step installs the skills into its Claude Code configuration directory, which is fresh on every round. It adds the configuration's refusals to `--disallowedTools`.
5. The summary comment on each ticket and the pass report name the configuration used, as `<repo>@<revision>`.

**Fail closed.** If the configuration cannot be cloned or is invalid, `select` reserves no ticket, and the error goes to the pass report. If a task names a revision that the agent step cannot read, the agent does not start. Without `--agent-config`, nothing changes: the agent gets the fixed refusals and the skills of its image.

## Schema

```text
agent.yaml                 # required
skills/<name>/SKILL.md     # optional, one directory per skill
skills/<name>/...          # any other file the skill reads
```

```yaml
version: 1                 # required, only 1 is supported
deniedTools:               # optional, added to the fixed refusals, in --disallowedTools syntax
  - "Bash(npm publish*)"
  - "WebFetch"
```

Rules:

- **Strict parsing.** An unknown field, an empty refusal or a refusal containing a comma makes the configuration invalid.
- **Only additions.** The fixed refusals of the harness (pushing, merging, editing a ticket or change request body) stay in the code. A configuration cannot remove them, and nothing in the schema can widen the allowed tools.
- **Skills** are directories named with lowercase letters, digits and dashes, each holding a `SKILL.md`. A skill of the image with the same name keeps precedence, since the image installs managed skills.
- **Regular files only.** A symbolic link or any other special file makes the configuration invalid. A configuration holds at most 500 files and 10 MiB.
- Nothing else in the repository reaches the agent: a README or CI files stay in the repository.

## The configuration repository is a forbidden zone

No pass ever writes to the configuration repository. `select` refuses it as a candidate `--repo` (usage error, exit code 2). As defence in depth, Publication refuses to push to it, with a zone refusal. A change to the configuration reaches the agent only through a change request merged by a maintainer.

**Condition: branch protection.** A merge by a maintainer is only guaranteed if the default branch is protected: no direct push, and a required review before merging. On GitHub, branch protection on a private repository needs a paid plan, so use a public repository or a paid plan. Without protection, anyone with write access to the configuration repository decides what guards the agent.

## Example

```sh
NIGHT_SHIFT_GITHUB_TOKEN=… ./night-shift select --repo owner/repo --agent-config owner/agent-config \
  --state /state --work /work
```

With the `CronJob` or the Compose example, add `--agent-config` to the `select` arguments. The option is not in a release yet: it needs images built from `main` (`make images`, tag `dev`) until the next one.
