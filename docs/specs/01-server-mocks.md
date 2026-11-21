# Server mocks

Part of [server requirements](01-server.md): `wrench mock llm` and `wrench mock gitlab` give the demo and the tests
an OpenRouter and a GitLab without the internet.

## Decisions

- The OpenRouter mock is part of the binary, test-control endpoints included. The control endpoints exist only in
  mock mode, never in `run server`.
- One OIDC implementation: `internal/mock/gitlab` holds the protocol core and does not import `testing`;
  `internal/gitlab/gitlabtest` is a thin test wrapper over it.
- The GitLab sign-in page is an account picker without a password: buttons for `users[].email` from the configuration
  plus a free-form email field. It is a demo tool and grants any listed or typed identity to whoever reaches it.
- The mock separates the browser-facing URL from the issuer, so containers and the host's browser use different
  addresses without network tricks.

## Layout

- `internal/mock/llm` — the OpenRouter mock: `Handler() http.Handler`.
- `internal/mock/gitlab` — OIDC issuer, code exchange, Users API, account picker.
- `internal/gitlab/gitlabtest` — test wrapper: `httptest.Server`, in-memory accounts, failure knobs, `Authorize`.
- `internal/app/mocks` — runs the mocks and the demo seed; `cmd/wrench` parses their flags.

## `mock llm`

| Flag       | Default | Purpose         |
| ---------- | ------- | --------------- |
| `--listen` | `:8000` | Listen address. |

- `POST /api/v1/chat/completions` streams or answers a completion; the last user message selects a scenario
  (`break`, `slow`, `nocost`, tool result).
- `GET /api/v1/generation` — generation metadata; `GET /api/v1/models` — the demo models with prices;
  `GET /api/v1/key` — key info with `limit_remaining`.
- `GET /_last`, `GET /_cancelled`, `GET /_generation_auth` — test control; `GET /healthz`.

## `mock gitlab`

| Flag           | Default                              | Purpose                                                    |
| -------------- | ------------------------------------ | ---------------------------------------------------------- |
| `--listen`     | `:9000`                              | Listen address.                                            |
| `--issuer`     | `http://127.0.0.1:9000`              | Issuer and base URL of every server-to-server endpoint.    |
| `--public-url` | value of `--issuer`                  | Base URL of `authorization_endpoint`, opened by browsers.  |
| `--config`     | `$WRENCH_CONFIG`, then `config.yaml` | Wrench configuration; its `users[].email` fill the picker. |

The client ID, client secret and sync token come from the server's variables `WRENCH_GITLAB_CLIENT_ID`,
`WRENCH_GITLAB_CLIENT_SECRET` and `WRENCH_GITLAB_SYNC_TOKEN`, so compose shares one block between both services. All
three are required.

Endpoints:

- `GET /.well-known/openid-configuration` — `issuer`, `token_endpoint`, `jwks_uri` under `--issuer`;
  `authorization_endpoint` under `--public-url`.
- `GET /oauth/discovery/keys` — JWKS of a key generated at startup.
- `GET /oauth/authorize` — validates `client_id`, `response_type=code`, `code_challenge_method=S256`, a non-empty
  `redirect_uri` and the `openid profile email` scopes, then renders the picker. Invalid requests get 400.
- `POST /oauth/authorize` — takes the chosen email and the original request parameters, issues a code bound to the
  nonce, PKCE challenge and redirect URI, and redirects to `redirect_uri?code=...&state=...`. An empty or malformed
  email re-renders the picker with an error.
- `POST /oauth/token` — client authentication, single-use code, redirect URI match, PKCE check; returns an RS256 ID
  token with `sub`, `email`, `email_verified=true` and the nonce.
- `GET /api/v4/users/{id}` — requires `PRIVATE-TOKEN`; answers `state: active` for any ID.
- `GET /healthz`.

The mock keeps no accounts. A user's ID is derived from the normalized email (a positive int64 from its SHA-256), so
it is stable across restarts, and the Users API reports every ID as active: a restarted mock never makes Wrench block
a user. Blocked, locked and deleted accounts are covered by `gitlabtest` only. The configuration is read once at
startup with the server's loader; an invalid configuration fails the command.

## Shared core

`internal/mock/gitlab` takes options (issuer, public URL, client credentials, sync token) and a `Directory`:

- `Lookup(id int64) (Account, bool)` — the account the Users API and the token endpoint report.
- `Candidates() []string` — the emails the picker lists.

The command uses a configuration-backed directory. `gitlabtest` uses an in-memory directory with `Add`/`Update`,
offers `StatusDown`, `Issuer`, `Audience` and `Nonce` overrides, and `Authorize(t, authURL, id)`, which issues a code
through the core without the picker.

## Demo

In `deploy/demo`, `openrouter` runs `mock llm` on `127.0.0.1:18000`; `gitlab` runs
`mock gitlab --issuer http://gitlab:9000 --public-url http://localhost:19000` on `127.0.0.1:19000`; `wrench` uses
`WRENCH_GITLAB_URL=http://gitlab:9000` and `WRENCH_OPENROUTER_URL=http://openrouter:8000/api/v1`. End-to-end tests
drive the sign-in without a browser: start at `/auth/gitlab/login`, follow the redirect to the mock (rewriting
`localhost:19000` to `127.0.0.1:19000`), post the picker with a configured email, follow the callback, and read
`/api/me` with the session cookie.
