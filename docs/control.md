# Control service

`night-shift control` is the **control service** for running passes without Kubernetes ([#109](https://github.com/UnPoilTefal/night-shift/issues/109), [ADR 0013](adr/0013-exemple-compose-second-systeme-d-execution.md)). It is a permanent service that:

- triggers passes on a cron schedule or on demand;
- runs at most one pass at a time;
- serves the stage relay of the trusted steps;
- keeps the history of passes.

It holds no forge token and no model token, and it makes no outbound call. Steps reach it on an internal network; the agent never does.

This is version 1. It only promises what this page documents: the control-plane contract is still to be set ([#103](https://github.com/UnPoilTefal/night-shift/issues/103)), and the forge webhook is not part of this version.

## `control serve`

```sh
night-shift control serve --history /history --transcripts /transcripts \
  --schedule "0 2 * * *" --listen :8080 --admin-listen :8081
```

| Option | Default | Meaning |
|---|---|---|
| `--schedule` | none | Five-field cron expression, in the container's time zone (UTC unless set; a `CRON_TZ=…` prefix is accepted). Without it, only manual requests trigger passes. |
| `--listen` | `:8080` | Address of the steps API, without authentication. Only the internal network of the trusted steps must lead there. |
| `--admin-listen` | disabled | Address of the read-only administration API. It requires a token, and should only be published on the host's `127.0.0.1`. |
| `--history` | required | Directory of the pass history: one `<pass>.json` file per closed pass. It never leaves the host. |
| `--transcripts` | none | Transcript archive written by `publish --transcripts`. The service only purges it. |
| `--retention` | `720h` (30 days) | Files older than this are purged from the history and the transcript archive, once a day. Nothing else is deleted. |
| `--watchdog` | `2h` | Longest a pass may run. Set it above the sum of the step timeouts, plus a margin. Past it, the pass is closed as `failed`, and the next pass may start. |
| `--requests` | `/run/night-shift/requests` | Directory of manual requests. It belongs to the service's container alone, for example a `tmpfs`. |

The administration token is `NIGHT_SHIFT_CONTROL_TOKEN`, or the file named by `NIGHT_SHIFT_CONTROL_TOKEN_FILE`. Setting both is a usage error (exit code 2).

## `control trigger`

```sh
docker compose exec control night-shift control trigger
```

This command requests a pass without network or secret: it drops a request file into the `--requests` directory, and the service picks it up within a second. Requests received while a pass is running, or while one is already waiting, merge into **one** waiting pass ([ADR 0008](adr/0008-declencheurs.md)).

## Steps API

The stages relay contract v1 ([contract](https://github.com/UnPoilTefal/night-shift/issues/108#issuecomment-6442592125), package `internal/relay`). The routes serve no authentication: only the trusted steps' network leads there.

### `POST /v1/passes/claim?wait=<duration>`

Called by `select`, which waits for a requested pass. The wait is capped at 2 minutes.

- `200` returns `{"pass": "pass-20261027T020000Z", "trigger": "schedule" | "manual", "requested": "<RFC 3339>"}`. The pass becomes active.
- `204` means no pass was requested during the wait. The client claims again.

There is never more than one active pass. Pass identifiers keep the format of the steps (`pass-<UTC timestamp>`), and the service never hands out the same one twice.

### `POST /v1/passes/{pass}/events`

Called by `select`, `publish` and `retry-publish`.

```json
{"stage": "publish", "type": "pass-finished", "at": "<RFC 3339>", "report": { }}
```

| `type` | Extra field | Meaning |
|---|---|---|
| `started` | | The step starts its turn. |
| `finished` | `exit` | The step finished its turn, with its exit code. |
| `incident` | `detail` | Full detail of an incident. It is kept in the history, never in the digest. |
| `pass-finished` | `report` | The full pass report, in the shape of `state/report.json`. It closes the pass. |

The responses are:

- `204`: the event is recorded;
- `400`: the event is invalid;
- `404`: the pass is unknown;
- `409`: the pass is already closed, either by `pass-finished` or by the watchdog;
- `500`: the history could not be written.

## Administration API

The administration API is read-only. Every request needs the header `Authorization: Bearer <token>`. A missing or wrong token gets `401`, and the response never quotes any token. Without a configured token, the API refuses every request.

| Route | Returns |
|---|---|
| `GET /v1/admin/status` | The active pass and its events so far, the waiting pass, and the next scheduled time |
| `GET /v1/admin/passes` | Closed passes, most recent first: identifier, trigger, start, end, status (`finished` or `failed`), number of tickets and incidents, and the URL of the full record |
| `GET /v1/admin/passes/{pass}` | The full record: requested, started and finished times, status, reason of a failure, and every event received. Detailed incidents are there. The `pass-finished` event carries the report: tickets with their outcome, repositories left out, and incidents. |
