# Deployment layout

Part of [server requirements](01-server.md): `deploy/demo`, `deploy/local` and `deploy/production`, `make demo`,
and demo data.

## Decisions

- `deploy/` lives at the repository root; shared pieces live in `deploy/shared/`.
- Each environment keeps its configuration in a `config/` subdirectory and mounts only that directory at
  `/etc/wrench` (read-only), so edits reach the container however the file is saved and
  [reload](01-server-reload.md) works in Docker. `.env`, the compose file and the README stay outside the mount: no
  container sees other services' secrets, and the backup (which copies every file of the configuration directory)
  never stores them.
- `deploy/shared/config/config.yaml` is the reference configuration for tests and the image. Each environment keeps
  its own copy.
- `.env` and `env.example` use the variable names of the [server requirements](01-server.md) table. Compose passes
  each container only its own variables: the OpenRouter key only to `wrench`, S3 credentials only to `backup`.
- Demo data comes from `wrench mock seed`, a one-shot service that fills an empty database.

## Layout

```
deploy/
  shared/      config/config.yaml, postgres/initdb/, caddy/Caddyfile
  demo/        docker-compose.yaml, README.md, config/config.yaml
  local/       docker-compose.yaml, README.md, env.example, config/config.yaml.example
  production/  docker-compose.yaml, README.md, env.example, config/config.yaml.example
```

`deploy/{local,production}/config/config.yaml` and every `deploy/*/.env` are ignored by Git.

## Environments

| Environment  | Services                                      | Access                              | Secrets                            |
| ------------ | --------------------------------------------- | ----------------------------------- | ---------------------------------- |
| `demo`       | postgres, LLM mock, GitLab mock, wrench, seed | `http://localhost:18080`, HTTP-only | none (synthetic values in compose) |
| `local`      | postgres, wrench                              | `http://127.0.0.1:8080`, HTTP-only  | `.env`                             |
| `production` | caddy, wrench, postgres, backup               | `https://$WRENCH_DOMAIN`            | `.env`                             |

- Demo and local set `WRENCH_HTTP_ONLY=true`; production leaves it `false`.
- In demo the LLM mock listens on `127.0.0.1:18000` and the GitLab mock on `127.0.0.1:19000`, with the demo
  configuration's users.
- Production networks: `edge` (Caddy and Wrench; Wrench reaches OpenRouter, GitLab and the webhook through it),
  `backend` (internal: Wrench, PostgreSQL, backup) and `egress` (backup to S3). Only Caddy publishes ports.
  Third-party images are pinned by digest.
- `deploy/shared/postgres/initdb` creates the read-only `wrench_backup` role with `WRENCH_BACKUP_DB_PASSWORD`, which
  must match `WRENCH_BACKUP_POSTGRES_DSN`. `restore` takes the owner's DSN through the same variable for that run.

## Demo data

`wrench mock seed [--config path]` waits until the schema exists, then fills it only when `attempts` is empty: 30
days of sessions and attempts for the configuration's users and models (tokens, confirmed costs and a few errors),
three device keys per user (active, revoked, expired), sessions with a device and a client agent, and the first
user's current 5-hour window at 85% of its limit, so the alert runner raises a real budget warning. It writes no
incidents (the runner owns them) and no unknown cost. Users are bound to the GitLab mock's identity, so a later
sign-in finds the seeded row. A second run changes nothing. The demo compose file runs it once after `wrench` is
healthy; `make e2e` starts the stack without it (compose profile), so tests see an empty journal.

## Make targets

`make demo` (start, wait, print `http://localhost:18080`), `make demo-down`, `make demo-reset`, `make demo-logs`.
`make e2e` uses `deploy/demo` under its own project; `make prod-config` validates `deploy/production` against its
`.env` or `env.example`.
