# Server requirements

One Go binary, `wrench`, runs the server, the backups and the development mocks. Details live in the part designs:
[CLI](01-server-cli.md), [mocks](01-server-mocks.md), [image](01-server-image.md),
[configuration format](01-server-config.md), [reload](01-server-reload.md), [monthly window](01-server-month.md),
[sign-in](01-server-auth.md) and [deployment layout](01-server-deploy.md).

## Commands

- `wrench run server` — serves the API, the `/v1` proxy and the dashboard;
- `wrench run backup` — runs scheduled backups until stopped;
- `wrench backup` — runs one backup;
- `wrench restore` — restores a snapshot into an empty database;
- `wrench mock llm` — OpenRouter-compatible LLM mock;
- `wrench mock gitlab` — GitLab mock (OIDC with an account picker, Users API);
- `wrench mock seed` — fills an empty demo database;
- `wrench healthcheck <url>`, `wrench version`.

## Packaging

One distroless, non-root image with `wrench`, `restic`, `pg_dump` and `pg_restore`. Version: Git tag plus commit hash.

## Deployment

```
deploy/
  shared/      caddy/Caddyfile, postgres/initdb/, config/config.yaml (reference)
  demo/        docker-compose.yaml, README.md, config/config.yaml
  local/       docker-compose.yaml, README.md, env.example, config/
  production/  docker-compose.yaml, README.md, env.example, config/config.yaml.example
```

- `demo` — mocks, seeded data, HTTP only; `make demo`.
- `local` — real GitLab and OpenRouter, `.env`, HTTP only.
- `production` — Caddy with HTTPS on an external domain, PostgreSQL, Wrench and backup; `.env`.

`WRENCH_HTTP_ONLY=true` serves deployments without TLS (cookies lose `Secure`); production keeps it `false`.

## Configuration file

`config.yaml` holds models, budgets, tiers, users and alerts; the server reloads it on change. The reference copy is
[deploy/shared/config/config.yaml](../../deploy/shared/config/config.yaml):

```yaml
models:
  opus-5.5:
    upstream_id: anthropic/claude-opus-5.5
    reasoning_effort: low
  glm-5.3:
    upstream_id: z-ai/glm-5.3
    reasoning_effort: low
  qwen3-coder:
    upstream_id: qwen/qwen3-coder
  deepseek-v4-flash:
    upstream_id: deepseek/deepseek-v4-flash
    reasoning_effort: none

budget:
  currency: USD
  timezone: UTC
  global_limits:
    5_hours: 8.00
    day: 16.00
    week: 80.00
    month: 200.00
    warn_at_percent: [80, 95]

tiers:
  basic:
    default: true
    budget: { 5_hours: 0.50, day: 1.00, week: 5.00, month: 20.00 }
    models:
      - name: glm-5.3
        default: true
      - name: qwen3-coder
  premium:
    budget: { 5_hours: 1.00, day: 5.00, week: 15.00, month: 50.00 }
    models:
      - name: opus-5.5
        default: true
      - name: glm-5.3

users:
  - email: user1@example.com # role user, default tier
  - email: user3@example.com
    role: admin
  - email: user4@example.com
    tier: premium

alerts:
  window: 15m
  repeat_after: 1h
  low_balance: { threshold: 5.00 }
  api_errors: { threshold: 20 }
  llm_first_token: { threshold_p95: 30s }
```

## Environment variables

| Variable                                                                                 | Component  | Description                                                     |
| ---------------------------------------------------------------------------------------- | ---------- | --------------------------------------------------------------- |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`                                      | PostgreSQL | Owner account and database                                      |
| `WRENCH_BACKUP_DB_PASSWORD`                                                              | PostgreSQL | Password of the read-only backup role (matches the backup DSN)  |
| `WRENCH_DOMAIN`                                                                          | Caddy      | Public domain; Caddy obtains its certificate                    |
| `WRENCH_CONFIG`                                                                          | server     | Path of `config.yaml` (image default `/etc/wrench/config.yaml`) |
| `WRENCH_LISTEN_ADDR`                                                                     | server     | Listen address                                                  |
| `WRENCH_PUBLIC_URL`                                                                      | server     | Public URL (OIDC callback, device login, opencode provider)     |
| `WRENCH_HTTP_ONLY`                                                                       | server     | `true` drops `Secure` from cookies; never in production         |
| `WRENCH_WEB_SESSION_TTL`                                                                 | server     | Dashboard session lifetime                                      |
| `WRENCH_CLIENT_KEYS_TTL`                                                                 | server     | Device key lifetime                                             |
| `WRENCH_POSTGRES_DSN`                                                                    | server     | Owner connection                                                |
| `WRENCH_GITLAB_URL`                                                                      | server     | GitLab instance                                                 |
| `WRENCH_GITLAB_CLIENT_ID`, `WRENCH_GITLAB_CLIENT_SECRET`                                 | server     | OIDC application                                                |
| `WRENCH_GITLAB_SYNC_TOKEN`                                                               | server     | `read_api` token for account-state checks                       |
| `WRENCH_GITLAB_SYNC_INTERVAL`                                                            | server     | Account-state check interval                                    |
| `WRENCH_OPENROUTER_URL`                                                                  | server     | OpenRouter API base (default `https://openrouter.ai/api/v1`)    |
| `WRENCH_OPENROUTER_KEY`                                                                  | server     | OpenRouter key; held only by Wrench                             |
| `WRENCH_UPSTREAM_TIMEOUT_CONNECT`, `_FIRST_TOKEN`, `_STREAM_IDLE`, `_TOTAL`              | server     | Upstream timeouts                                               |
| `WRENCH_UPSTREAM_CONCURRENCY_PER_USER`, `_GLOBAL`                                        | server     | Concurrent upstream requests                                    |
| `WRENCH_SESSION_OFFLINE_AFTER`                                                           | server     | Silence after which a session is `offline`                      |
| `WRENCH_ALERT_WEBHOOK_URL`                                                               | server     | Slack/Mattermost-compatible webhook (optional)                  |
| `WRENCH_ORG_NAME`                                                                        | server     | Organization name opencode shows (default `Wrench`)             |
| `WRENCH_LOG_LEVEL`                                                                       | server     | `debug`, `info`, `warn` or `error`                              |
| `WRENCH_BACKUP_POSTGRES_DSN`                                                             | backup     | Read-only backup role connection                                |
| `WRENCH_BACKUP_SCHEDULE`                                                                 | backup     | 5-field cron, UTC                                               |
| `WRENCH_BACKUP_RETENTION`                                                                | backup     | `restic forget --keep-within` value                             |
| `WRENCH_BACKUP_RESTIC_PASSWORD`                                                          | backup     | Repository password; kept outside the node                      |
| `WRENCH_BACKUP_S3_ENDPOINT`, `_BUCKET`, `_PATH`, `_REGION`, `_ACCESS_KEY`, `_SECRET_KEY` | backup     | S3 repository                                                   |
| `WRENCH_BACKUP_HEARTBEAT_URL`                                                            | backup     | Optional dead-man switch pinged after each backup               |
