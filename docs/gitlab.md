# Running night-shift on GitLab

night-shift talks to GitLab through its **GitLab adapter**, which serves both the ticket source and the code forge contracts ([ADR 0012](adr/0012-outils-derriere-des-contrats.md)). The pass itself is the same as on GitHub. This guide covers what differs: the token, branch protection, network egress, an internal certificate authority, and where a pass may run.

It works with gitlab.com and with a dedicated or self-managed instance. Nobody has run it against a real instance yet: that acceptance belongs to the maintainer ([#57](https://github.com/UnPoilTefal/night-shift/issues/57)). Points still to confirm there are marked *to confirm*.

## Deploy the pass

Start from [`examples/solo/cronjob-gitlab.yaml`](../examples/solo/cronjob-gitlab.yaml). It is the same pod as the GitHub example, with:

- `--forge gitlab` on `select` and both `publish` steps;
- `--instance https://gitlab.example.com` on the same steps. The adapter derives the API (`<base>/api/v4`) and the clone addresses (`<base>`) from that one base, relative root included (`https://example.com/gitlab`), so the token can never be sent to another host. Remove it for gitlab.com;
- `--repo` with the full project path, subgroups included: `group/subgroup/project`;
- the token in `NIGHT_SHIFT_GITLAB_TOKEN`, mounted **only** in `select` and both `publish` steps. A test in the repository fails if an agent step receives it.

### Never run a pass in a CI job

A pass runs on a dedicated system: the example `CronJob`, later the operator. **Never run the agent step in a GitLab CI job** (nor in any CI runner):

- the runner injects `CI_JOB_TOKEN` into every job, and you cannot remove it from a single job. The runner also uses it to clone the repository, so unsetting the variable is not enough: the token stays valid as long as the job runs;
- a runner usually has open network egress, with no allow-list proxy, and the job sees the project's variables, cache and artifacts.

One job would then hold untrusted content (the ticket, through the brief), write access and network access at the same time, which is exactly what the separation of the steps prevents ([ADR 0011](adr/0011-publication-frontiere-des-zones.md): the guarantee lies in the deployment). The agent step refuses to start when `CI_JOB_TOKEN`, `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN`, `GL_TOKEN` or `NIGHT_SHIFT_GITLAB_TOKEN` is set, or when any variable holds a value that starts with a GitLab token prefix (`glpat-`, `gldt-`, `glcbt-`, `glptt-`, `glrt-`, `glagent-`, `glft-`, `glimt-`, `gloas-`). This refusal is intended. Do not work around it.

A scheduled pipeline may still *trigger* a pass, for instance by creating a Kubernetes Job. It must not *run* it.

## Token

Use a **project access token** with the **Developer** role. A group access token works for several projects, but gives the same rights on every project of the group.

- **Scopes**: `api` and `write_repository` (*to confirm*). GitLab has no scope limited to issues and repositories: writing to issues and merge requests requires `api`, which grants much more. The real limit comes from the **Developer role** and the **protected default branch** (below), not from the scope.
- **Availability**: on gitlab.com, project and group access tokens need a paid tier. On the Free tier, create a dedicated service account, invite it to the project as Developer, and use one of its personal access tokens.
- **Expiry**: access tokens expire (at most one year by default). Plan their rotation, or the pass fails on its first API call.
- **Identity**: night-shift signs its commits as `night-shift@users.noreply.<host>` and the agent's as `night-shift-agent@users.noreply.<host>`, the domain GitLab reserves for private addresses by default. Push rules that reject unverified users or commits whose author is not a GitLab user would reject these pushes (*to confirm*): leave them off for this project.

What the adapter does with the token: list and update issues, read the opt-in, clone, push the `agent/…` branch, open a draft merge request, and **read** pipeline results for the CI rounds. It never triggers a pipeline, never marks a merge request ready, and never merges.

## Branch protection

- Protect the **default branch**: *Allowed to merge* and *Allowed to push and merge* set to **Maintainers** (or *No one*), never Developers. The Developer token can then neither push to it nor merge a merge request.
- Do **not** protect `agent/*` branches: Publication must push them.
- Mark every CI/CD variable that holds a secret as **Protected**. GitLab only exposes protected variables to pipelines on protected branches, so a pipeline on an `agent/…` branch never sees them.
- In the job token settings, keep **Allow Git push requests to the repository** off, if your version has it.

A pipeline on an `agent/…` branch runs code the agent may have changed outside the forbidden zones (a `Makefile`, tests, scripts), with the job token of the user who pushed: night-shift's token account. This is accepted risk R1 of [ADR 0011](adr/0011-publication-frontiere-des-zones.md): such pipelines must hold no secret and no write permission. night-shift does not check it.

## Opt-in

Forbid `.gitlab-ci.yml` and every file it includes locally (`include: local:`, often under `.gitlab/` or `ci/`): see [the opt-in guide](opt-in.md#on-gitlab).

## Tickets

- **Ready tickets**: open issues labelled `ready-for-agent`, with their notes. System notes (label changes, mentions) are not comments.
- **Trusted authors**: project members with the **Developer** role or above, direct or inherited from a group, plus the accounts named in the opt-in's `trustedAuthors`. A Reporter or a Guest is a third party.
- **Blocked tickets**: an open issue linked as *is blocked by*, where your edition offers blocking links, or named on a `Blocked by: #12, #15` line of the issue body, on every edition.
- **Labels**: night-shift creates the lifecycle labels and `sensitive-zone` in the project when they are missing.

## Network egress

- `select` and both `publish` steps reach the instance over HTTPS (API and git), and `publish` reaches `discord.com` when the Discord webhook is set.
- The agent steps reach the model provider's API and the registries of the repository's toolchain, through the allow-list proxy. They **never** need the GitLab instance: leave it out of their allow-list.

## Internal certificate authority

TLS is always verified. Do not disable it (`GIT_SSL_NO_VERIFY` or similar). For an instance served by an internal certificate authority, add that authority to the certificate store of the image that runs `select` and `publish`:

```dockerfile
FROM ghcr.io/unpoiltefal/night-shift:<version>
USER root
COPY internal-root-ca.crt /usr/local/share/ca-certificates/internal-root-ca.crt
RUN update-ca-certificates
USER 65532
```

Both night-shift and git read the system store (`/etc/ssl/certs/ca-certificates.crt`).

Without a derived image, mount a bundle that holds **both** the public roots and your authority, and point `SSL_CERT_FILE` (night-shift) and `GIT_SSL_CAINFO` (git) at it in the trusted steps. A bundle that holds only your authority would break the calls to `discord.com`.

## Replaying the zone-refusal proof

On a sandbox project whose opt-in forbids `.gitlab-ci.yml`:

```sh
NIGHT_SHIFT_GITLAB_TOKEN=… make e9-replay E9_FORGE=gitlab E9_INSTANCE=https://gitlab.example.com \
  E9_REPO=group/sandbox E9_IMAGE=ghcr.io/unpoiltefal/night-shift:<version>
```

A simulated hostile agent returns a series that rewrites `.gitlab-ci.yml` with a job on `agent/…` branches. The target checks on the instance that:

- no `agent/…` branch, merge request or pipeline exists;
- the ticket went back to `ready-for-human`, with the path and the zone;
- the default branch did not move.

It needs `curl` and `jq` on the host, and cleans up everything it created.
