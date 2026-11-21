# Console facade

Wrench answers the OpenCode Console API ([contract](06-opencode.md)) so a vanilla opencode signs in with GitLab
through a device login and receives its provider, policies and budgets from Wrench.

## Device login

RFC 8628 device authorization, the only way a client gets a key.

- `POST /auth/device/code` accepts only `client_id: "opencode-cli"`. It answers `device_code`, a `user_code` like
  `ABCD-1234`, `verification_uri_complete = ${WRENCH_PUBLIC_URL}/device?code=<user_code>`, `expires_in: 600` and
  `interval: 5`. Table `device_codes` stores both codes as SHA-256 hashes with status (`pending`, `approved`,
  `denied`), the approving user, the last poll time and the expiry. At most 1000 unexpired codes exist; beyond that
  the endpoint answers `503`.
- `/device` is a dashboard page that needs the session cookie. Without one, the web sign-in runs and returns to
  `/device?code=...`. The page shows the code and Approve/Deny; `POST /api/device` requires the session cookie, a
  same-origin `Origin` and a user whose GitLab status is fresh.
- `POST /auth/device/token` with the device-code grant:
  - pending: 400 `authorization_pending`; polled faster than `interval`: 400 `slow_down`;
  - denied: 400 `access_denied`; expired or unknown: 400 `expired_token`;
  - approved: the code is consumed and Wrench issues a `wrk_` key as `access_token` for device
    `opencode-<random>` named `opencode`, a `refresh_token`, `expires_in` = the key's remaining lifetime
    (`WRENCH_CLIENT_KEYS_TTL`) and `org_id: "wrench"`.
- The refresh grant issues a new key for the same device, which revokes the previous one (one active key per user
  and device), and rotates the refresh token. A used, revoked or expired refresh token gets 400 `invalid_grant`.
  Refresh tokens are stored as hashes, bound to the device, and expire with `WRENCH_CLIENT_KEYS_TTL`. Blocked users
  get `invalid_grant`.

## Console API

Every Console endpoint authenticates the bearer `wrk_` key with the usual resolver (fresh GitLab status required)
and ignores `x-org-id`. They are declared in `api/openapi.yaml` under tag `opencode`.

- `GET /api/user`: `{id, email}` of the caller (`id` is the Wrench user id).
- `GET /api/orgs`: `[{id: "wrench", name}]`; `name` comes from `WRENCH_ORG_NAME` (default `Wrench`).
- `GET /api/v2/config`, with header `x-opencode-instructions-skills: 1`:
  - `providers.wrench`: `name: "Wrench"`, `package: "@opencode/ai/providers/openai-compatible"`,
    `settings.baseURL: ${WRENCH_PUBLIC_URL}/v1`, and `models` with the caller's tier models, each with `name` and
    `limit.context` / `limit.output` from the catalog (models the catalog lacks are left out);
  - `policies`: `provider.use` deny `*`, then allow `wrench`.
- `GET /api/v2/instructions`: `{instructions: []}`; opencode uses the project's own AGENTS.md.
- `GET /api/v2/skills`: `{skills: []}`.
- `GET /api/policies/current-budgets`: one rule per window with a limit:
  - the caller's windows as `scope: "each"`, `shared: false`;
  - global windows only for administrators, as `scope: "combined"`, `shared: true` (other users never see
    everyone's spend);
  - every rule `per: "set"`, `coverage: "all"`, `models: []`, `vendors: []`, `picked: false`, dollars from the
    exact `money.Amount`, `resetsAt` = the window end in RFC 3339. Spend is the same as in admission.
- `GET /zen/go/v1/usage`: 403.

## Proxy

- Session id: `X-Wrench-Session-Id`, else `X-Opencode-Session-Id`, else 400; 1–128 characters of
  `[A-Za-z0-9_.:-]` (opencode ids look like `ses_...`).
- Each request touches the session: the authenticating key (`sessions.client_key_id`, so the dashboard shows the
  device) and the leading product token of `User-Agent` as the session's `agent`.

## agent/opencode

- `README.md`: install opencode, sign in with
  `opencode auth login opencode --method device --answer server=https://<wrench>`, the supported opencode version
  (2.3.x), what to expect (`/usage`, only Wrench models; subagents appear as separate sessions; disable `/share`),
  and how to sign out.
- Contract test (Go, build tag `opencode`, target `make opencode-contract`): against the demo stack it runs the real
  opencode binary with isolated XDG directories, completes the device login by approving the code as a GitLab mock
  user, runs `opencode run`, and checks through the JSON API that the attempt was recorded under an opencode
  session. CI installs the pinned `opencode-ai` version.
