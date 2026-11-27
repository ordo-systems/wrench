# Wrench core

Company-managed coding-agent platform: vanilla opencode 2.3.x clients -> Go monolith `wrench` (access, budgets, models,
accounting, dashboard) -> OpenRouter. Delivery: four containers (Caddy, Wrench, PostgreSQL, backup).
Product requirements: `docs/specs/coding-agent-platform.md`; per-area designs `docs/specs/NN-*.md`; user docs `docs/*.md`.

## Layout (one Go module `github.com/ordo-systems/wrench` at the root)
- `cmd/wrench` — cobra command tree, one `newXxxCmd` per command calling `internal/app/*` directly (`run server|backup`,
  `backup`, `restore`, `healthcheck`, `version`, `mock llm|gitlab|seed`; usage errors exit 2).
- `api/` — `openapi.yaml` (source of truth, embedded as `api.Document`) + generated `api.gen.go` (strict server).
  `make generate` also writes `webapp/src/api.gen.ts` and `tests/internal/wrenchapi`; `make check-generated` in `lint`.
- `internal/` — server packages (below). `webapp/` — Vue 3 dashboard, built into `internal/web/dist` and embedded.
- `agent/opencode` — setup README + `opencode` contract test (build tag, `make opencode-contract`).
- `tests/` — black-box `e2e`, `livecheck`, `backup-drill`, `login`; JSON API only (`make check-blackbox`).
- `deploy/{shared,demo,local,production}`; `Dockerfile` at the root (one distroless nonroot image, UID 65532, with
  restic, pg_dump, pg_restore). `website/` — Pages placeholder.

## Source map (`internal/`)
- `app/server` — load config/env, open store, migrate, serve, graceful shutdown. `app/backup` — cron loop, one-off
  backup, restore (pg_dump + restic to local and S3, retention only after success, status per `backupstatus`).
  `app/mocks` — mocks and the demo `Seed`.
- `config` — `config.yaml` (models, budget, tiers, users, alerts); `Holder` (atomic snapshot + reload status) and
  `Watch` (fsnotify + 10s poll); invalid edits keep the old config and raise `config:invalid`. `configtest` fixture.
- `env` — runtime settings from the environment; `Secret` redacts formatting.
- `money` — exact USD `Amount` (int64 nano-dollars, never float64).
- `auth` — `Resolver`: bearer `wrk_` device key or the session cookie (`auth.Cookies`); re-checks YAML allowlist and
  stored GitLab state per request; inference needs fresh status.
- `gitlab` — OIDC (code+PKCE) and Users API status; `gitlabtest` wraps `mock/gitlab`.
- `login` — `/auth/gitlab/*` web sign-in, logout, device login (RFC 8628), status `Syncer`.
- `catalog` — OpenRouter `/models` (limits, prices) cached in `openrouter_models`; statuses ok/unknown_upstream/no_catalog.
- `probe` — background `GET key` reachability probe (30s) behind `/readyz` and the `openrouter` check.
- `proxy` — `/v1/chat/completions` relay to OpenRouter with usage/cost tap. `budget` — pure admission + `Checker`.
- `reconcile` — confirms/verifies costs from OpenRouter `/generation`; closes interrupted attempts.
- `alerts` — pure `evaluate` + runner; webhook or log. `events` — in-process SSE bus (topics, per-user delivery).
- `store` — pgx pool, embedded migrations, attempts journal, sessions, device codes, stats, incidents, usage windows.
- `httpapi` — implements `api.StrictServerInterface`; `guard` reads `security`/`x-wrench-admin` from the document;
  `/v1`, `/auth/*`, `/zen` and dashboard files are routed by hand. `web` — embedded SPA with history fallback.
- `mock/llm` (OpenRouter + test-control endpoints), `mock/gitlab` (OIDC + account picker behind a `Directory`).

## Invariants
- Tokens (keys, sessions, device and refresh codes, login binding) are stored only as SHA-256 hashes. One active key
  per (user, device).
- Identity binds email <-> (issuer, sub) once; mismatches are `ErrIdentityConflict`, never auto-rebind.
- Confirmed GitLab disable/removal => user blocked + all credentials revoked in one tx; unavailable checks change nothing.
- Cookie-authenticated mutations require same-origin `Origin`.
- Lists default to the caller (admins too); other users and `?user=*` only for administrators (`httpapi.scopeFor`),
  others get 403. `system`, `users`, `alerts`, user revocation are `x-wrench-admin`.
- Proxy forwards an allowlist of client fields; adds `usage.include`, `provider.allow_fallbacks=false`,
  `require_parameters=true`, catalog `max_tokens`/`reasoning_effort`, and `session_id` = client session (sticky
  routing for prompt caching; cache fields are allowlisted). Response bytes are relayed unchanged.
- Attempt (pending) is persisted with its start time before forwarding; start time decides the budget windows.
- Missing usage/cost is null/pending, never zero. Unknown costs never pause inference: they count as zero until
  reconciled and are reported per window (`unsettled`). A failed dial is `not_sent` ($0). Every cost statement goes
  to append-only `charge_events`.
- One logical request -> at most one upstream attempt; no retries or model substitution.
- Accounting storage unavailable -> reject new inference (fail closed).
- Never log prompts, completions, source code or secret values. The OpenRouter key lives only in `wrench`.
- SSE events carry topic names only; data is re-read through authorized REST.

Tooling/stack: `mem:tech_stack`. Commands: `mem:suggested_commands`. Code style and comment rules: `mem:conventions`.
Done-checklist and quality tools: `mem:task_completion`.
