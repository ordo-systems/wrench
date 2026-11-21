# Operations

Deploying, backing up, updating, restoring and monitoring a Wrench server.

## Secrets

`.env` holds the listen and public URLs, the database DSNs, GitLab credentials and timing, the OpenRouter key,
timeouts, concurrency, the log level and the backup settings ([variables](specs/01-server.md#environment-variables)).
Never paste its values into diagnostics or reports. Missing secrets stop startup; there is no anonymous fallback.
Only Wrench receives the OpenRouter key (set a credit limit on it in OpenRouter); only the backup receives the S3
credentials. Third-party images are pinned by digest.

## Deployment and recovery

[deploy/production/docker-compose.yaml](../deploy/production/docker-compose.yaml) runs the four containers. Third-party images are pinned by
digest; `wrench` and `backup` run one image built from the checked-out commit. Only Caddy publishes ports (80 and 443) and
obtains the certificate for `WRENCH_DOMAIN`. PostgreSQL and backup sit on an internal network;
backup additionally reaches the internet for S3, Wrench for OpenRouter, GitLab and the alert webhook. Each container receives only its own variables from `.env`; only `config/` is mounted into Wrench and
the backup, so `.env` never reaches a container or a backup.

### Install

1. Point DNS for `WRENCH_DOMAIN` to the host and open ports 80 and 443.
2. In `deploy/production/`, copy `env.example` to `.env` and replace every placeholder; copy
   `config/config.yaml.example` to `config/config.yaml` and adjust it;
   use letters and digits for `POSTGRES_PASSWORD` and `WRENCH_BACKUP_DB_PASSWORD`, and repeat them inside
   `WRENCH_POSTGRES_DSN` and `WRENCH_BACKUP_POSTGRES_DSN`.
   Store `WRENCH_BACKUP_RESTIC_PASSWORD` and the S3 credentials outside the node: they are needed to restore.
3. `make prod-config` validates the compose file against that `.env`.
4. `export WRENCH_BUILD_VERSION=$(git describe --tags --always --dirty) WRENCH_BUILD_COMMIT=$(git rev-parse --short HEAD)`
   (shown in the Overview status widget and in `wrench version`), then
   `docker compose -f deploy/production/docker-compose.yaml up -d --build`.
5. Check `https://<domain>/healthz`, sign in, and run a first backup:
   `docker compose -f deploy/production/docker-compose.yaml exec backup wrench backup`.
6. Configure external monitoring (below).

### Backups

`backup` runs `WRENCH_BACKUP_SCHEDULE` (5-field cron, UTC) inside the container; no host cron is used.
Each run dumps the database with the read-only `wrench_backup` role from `WRENCH_BACKUP_POSTGRES_DSN`
(`pg_dump --format=custom`; the DSN reaches the tools as libpq variables, never on a command line), checks the
dump with `pg_restore --list`, and stores it with every file of the configuration directory (`config.yaml`) in the local
repository (volume `backup_repo`) and in `s3:<endpoint>/<bucket>/<path>`. Retention
(`restic forget --keep-within $WRENCH_BACKUP_RETENTION --prune`) and `restic check` run per repository only
after its backup succeeded, so a failed run never removes the last good snapshot. Client sessions and
`.env` are not backed up. restic 0.20.0 encrypts both repositories with `WRENCH_BACKUP_RESTIC_PASSWORD`, which must
be kept outside the node. Target RPO is one backup interval; target RTO is four hours, proven by the restore drill.
The status file is shown in the Overview status widget; Wrench alerts when the last run failed, when no backup succeeded
by two hours after the scheduled time, or when the container never reported. `WRENCH_BACKUP_HEARTBEAT_URL`
receives a ping after each success (`/fail` after a failure).

### Update and migrations

Back up first (`wrench backup` in the backup container), then `git pull` the release, export
`WRENCH_BUILD_VERSION` and `WRENCH_BUILD_COMMIT` as in install step 4, and
`docker compose -f deploy/production/docker-compose.yaml up -d --build`.
To run a published image instead of building one, `docker pull ghcr.io/ordo-systems/wrench:<version>`, tag it
`wrench:<version>`, set `WRENCH_VERSION=<version>` in `.env`, and run `up -d` without `--build`.
Wrench validates configuration before starting and applies forward-only embedded migrations at start;
in-flight requests drain for up to `WRENCH_UPSTREAM_TIMEOUT_TOTAL`. `config.yaml` changes apply without a restart (see [Configuration](configuration.md#files)).

### Rollback

Without a schema change, check out the previous release and rebuild. Migrations are not reversible: after a
release that changed the schema, roll back by restoring the backup taken before the update (below) with the
previous release checked out.

### Restore

On a clean node with the checkout, `deploy/production/.env` and the restic password:

1. `docker compose -f deploy/production/docker-compose.yaml up -d postgres` (an empty database).
2. Restore the newest S3 snapshot into it as the owner:
   `docker compose -f deploy/production/docker-compose.yaml run --rm -e WRENCH_BACKUP_POSTGRES_DSN=<WRENCH_POSTGRES_DSN> backup restore --from s3 latest`.
   The policy files of that snapshot are printed into the container's `/restore/config`; compare them with the
   checkout. Use a snapshot ID instead of `latest` for an older state, `--from local` when the local repository survived.
3. `docker compose -f deploy/production/docker-compose.yaml up -d` and check the dashboard.

Start Wrench only after the restore: on an empty database it would apply migrations first.
`make backup-drill` performs this procedure end to end against a stand-in S3 (SeaweedFS): it destroys the
database and the local repository and compares the restored data with the original. Repeat a drill against
the real bucket monthly.

### External monitoring

A stopped node cannot alert. Monitor `https://<domain>/healthz` from outside (for example Uptime Kuma or a
hosted uptime check) and use a dead-man check for `WRENCH_BACKUP_HEARTBEAT_URL`.

## Releases

GitHub Actions ([.github/workflows](../.github/workflows)) checks and publishes:

| Workflow  | Runs on                                                  | Does                                                                                                                              |
| --------- | -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `ci`      | pull requests and pushes to branches other than `master` | `make ci`, `make vuln`, an image build for amd64 and arm64 (not pushed), `make e2e`, `make opencode-contract` with opencode 2.3.0 |
| `image`   | pushes to `master`                                       | the checks, then `ghcr.io/ordo-systems/wrench:edge` and `:sha-<7 hex>`                                                                 |
| `release` | tags `v*`                                                | the checks, the image with version tags, a draft GitHub Release                                                                   |
| `pages`   | pushes to `master` that change `website/`, or manually   | deploys `website/site/` to GitHub Pages                                                                                                |

To release, tag a commit on `master` and push the tag:

```sh
git tag v1.2.0 && git push origin v1.2.0
```

`v1.2.0` publishes the image as `1.2.0`, `1.2` and `latest`; a pre-release such as `v1.2.0-rc.1` publishes only
`1.2.0-rc.1` and makes a pre-release draft with generated notes. The image is pushed only after the checks
passed; if the release job itself fails, the image is already published, and a re-run may leave a second draft to
delete. Review the draft and press **Publish**. Every image has a build provenance attestation:

```sh
gh attestation verify oci://ghcr.io/ordo-systems/wrench:1.2.0 --repo ordo-systems/wrench
```

One-time repository settings: Settings → Pages → Source: **GitHub Actions**; and, for anonymous pulls, the
`wrench` package's visibility on GHCR set to public (it starts private).

Actions are pinned to commit SHAs; `make lint-workflows` (part of `make lint`) runs actionlint, checks the pins
and tests the release tag script. A workflow change is first exercised for real on GitHub after it is pushed.

## Server commands

The image (distroless, UID 65532) runs one binary, `wrench`, and also ships `restic` 0.20.0, `pg_dump` and
`pg_restore` (PostgreSQL 17); services differ only in their command. Usage errors exit 2, runtime errors exit 1.

| Command                                                                          | Purpose                                                                                                   |
| -------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `run server [--config path]`                                                     | Serve the API, proxy and dashboard; `--config` defaults to `WRENCH_CONFIG`, then `config.yaml`            |
| `run backup`                                                                     | Run backups on `WRENCH_BACKUP_SCHEDULE` until stopped                                                     |
| `backup`                                                                         | Run one backup and exit                                                                                   |
| `restore [--from s3\|local] [--config-out dir] [snapshot]`                       | Restore a snapshot (default `latest`) into an empty database                                              |
| `healthcheck <url>`                                                              | Probe a URL (container health checks; the image has no shell)                                             |
| `version`                                                                        | Print the version (`git describe` and commit, set at build time)                                          |
| `mock llm [--listen :8000]`                                                      | OpenRouter-compatible LLM mock with test-control endpoints (`/_last`, `/_cancelled`, `/_generation_auth`) |
| `mock gitlab [--listen :9000] [--issuer url] [--public-url url] [--config path]` | GitLab mock: OIDC issuer, Users API (every account active) and an account picker                          |

`mock gitlab` reads the client ID, secret and sync token from `WRENCH_GITLAB_CLIENT_ID`, `WRENCH_GITLAB_CLIENT_SECRET`
and `WRENCH_GITLAB_SYNC_TOKEN`, like the server. Its picker lists `users[].email` from the configuration plus a
free-form email field and signs in without a password: it is for demos and tests only. `--issuer` is the URL the
server reaches (default `http://127.0.0.1:9000`); `--public-url` is the URL browsers open for sign-in (default the
issuer). Mock account IDs derive from the email, so they survive restarts.

## Dashboard and alerts

The dashboard is a single-page app with these tabs:

| Tab      | Who            | Content                                                                                                                                                                                                                              |
| -------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Overview | administrators | Two by two: system status (checks, backup, config revision) and the shared budget, every model with context and prices and the tiers; then usage of everyone                                                                         |
| Usage    | everyone       | Own tier's models with price and today's requests and spend, own budget windows and usage cards; administrators can pick a user or all users                                                                                         |
| Sessions | everyone       | Own sessions, newest activity first, with Active/Offline filter and agent; administrators pick a user or all users                                                                                                                   |
| Keys     | everyone       | Own device keys; New device key (shown for your own keys only) gives the login steps and command; administrators pick a user or all users. Revoke and Revoke all ask for confirmation first                                          |
| Users    | administrators | Users with tier, sign-in state (`never signed in` until the first sign-in), keys and a percent, amount and bar per budget window; Revoke all keys (after confirmation) ends every key and session of a user who has signed in        |
| Alerts   | administrators | Incidents with severity and status filters (open by default), open first; click a row or press Enter or Space on it to open its details (full message, incident key and times; Escape closes); the tab shows the number of open ones |

Users have no request journal of their own: their usage is in the Usage tab and their sessions in Sessions.
An icon in the top bar shows whether requests run: Running, Degraded (a system check is not ok; administrators only),
Paused or Unknown, named in its tooltip. Clicking it lists the pause reasons and, for administrators, the system checks.
The dashboard updates itself: the server announces changes over `GET /api/events` and each widget or table
re-reads its data (at most once a second per topic), so there is no Refresh button. The Overview and Usage
cards state their period (`24h`, `7d`, `30d`) and show it as one strip of cells (the last 24 hours, the last 168
hours or the last 30 days, in UTC like the bucket starts), whose cells shade the accent color in four steps of the period's maximum and title their time and value; each card opens
a detail chart. The period (24 hours, 7 days or 30 days) and model are filters, and charts and grids come from
`GET /api/stats`. The "LLM requests" card counts attempts by outcome. The Usage Models card lists the caller's
models with the context, the catalog price in and out per one million tokens (`—` when unknown) and the caller's requests and
spend, with a bar against the day's largest, per model in the current day budget window (`GET /api/usage?window=day&group=model`). A period without requests shows `—` on every card; otherwise each card shows
its value, zero included, and only a percentile without observations stays `—`. On Usage and its detail charts an
administrator's user filter (`?user=<email>`, `?user=*` for all users, none for themselves) picks whose budget and
usage are shown; the by-user split of a detail chart is offered for all users only. The budget widgets show each
window counting down to its reset ("resets in 2h 30m", the exact UTC time in the tooltip). An hourglass beside a window, in the budget widgets and the Users table, means
finished requests in that window still have an unknown cost; they are not counted yet, so the window may be
overrun slightly. The tooltip gives the count; nobody has to act on it.
In detail charts missing telemetry is a gap with a note on how many attempts reported it, never zero; costs are confirmed costs,
with the number of unknown ones shown beside them.
Active sessions are counted from requests in 5-minute steps.

The optional `alerts` section of `config.yaml` enables checks every minute: budget thresholds
(`warn_at_percent`) and exhaustion per user and globally, OpenRouter unreachable with Wrench's key (from the readiness probe), accounting storage unavailable,
configured models unavailable or a model catalog older than 24 hours, and one alert per optional subsection:
`low_balance.threshold` (the remaining credit limit of `WRENCH_OPENROUTER_KEY`, `limit_remaining` from
`<WRENCH_OPENROUTER_URL>/key`; the key needs a credit limit in OpenRouter, otherwise the alert stays silent and
Wrench logs a warning),
`api_errors.threshold` (percent of failed requests over `window`, once `min_attempts` finished, default 5),
and `llm_first_token.threshold_p95` (p95 time to first token over `window`, once `min_attempts` observed,
default 5). An omitted subsection disables its alert.
A continuing condition is one incident: it is announced when it opens, repeated at most every `repeat_after`
and announced again when it resolves (only if its opening was delivered). Undelivered notifications are
retried on the next check. `WRENCH_ALERT_WEBHOOK_URL` receives `{"text": ...}` (Slack and Mattermost
incoming webhooks); without it alerts are logged and shown on the dashboard. A stopped node cannot alert:
use [external monitoring](#external-monitoring) for node failure.

## Pilot

1. Deploy (above) with real GitLab, OpenRouter and S3 settings and run the `make backup-drill` procedure once
   against the real bucket.
2. Run the live check with an admin's device key; it costs a few short requests per model:
   `WRENCH_TOKEN=wrk_... make livecheck WRENCH_URL=https://<domain>`. It streams a tool call, answers it and
   cancels a stream for every tier model, and waits
   until every attempt has a confirmed cost. "close after cancel" shows how long the proxy took to close a
   cancelled attempt.
3. Connect opencode 2.3.x ([agent/opencode](../agent/opencode/README.md)) for two or three people on their own machines (macOS, Linux and Windows if used) and
   work on real tasks for a week. Watch the dashboard overview (spend, cache share, latency, extra attempts,
   unknown costs), the Alerts tab and client problems.
4. Tune budgets and tier models in the deployment's `config/config.yaml` from what was measured (applied without a restart), and open access to the team.
