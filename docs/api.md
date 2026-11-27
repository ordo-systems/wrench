# HTTP API

The JSON API of `wrench run server` is described by one OpenAPI 3.0 document,
[`api/openapi.yaml`](../api/openapi.yaml). It lists every route under
`/api`, `/healthz`, `/readyz` and the device login (`POST /auth/device/code`, `POST /auth/device/token`), with
parameters, request bodies, every response and the shapes of all JSON objects. The server routing, the dashboard's
TypeScript types and the Go client of the end-to-end tests are generated from it; see [Development](development.md#api-contract).

## Credentials

- **Device key** (`Authorization: Bearer wrk_...`), issued to opencode by the device login (`POST /auth/device/token`).
- **Dashboard session cookie** (`__Host-wrench_session` over HTTPS, `wrench_session` otherwise), set by GitLab
  sign-in. Requests other than `GET` authenticated by the cookie must carry the server's public `Origin`.

Operations that declare `security` need one of them; `GET /healthz`, `GET /readyz` and the device login need none;
`POST /api/device` (approving a device) takes only the dashboard session. Operations marked `x-wrench-admin: true` (the system view, the users list, alerts,
revoking a user) answer `403` to everyone but administrators. Both checks run before
the request is parsed, so a request without a valid credential always gets `401`.

## Whose data

Every list (`/api/requests`, `/api/sessions`, `/api/usage`, `/api/stats`, `/api/keys`, and the
admin-only `/api/alerts`) defaults to the caller's own data, administrators included. `?user=` widens it:

| `?user=`           | user     | administrator    |
| ------------------ | -------- | ---------------- |
| empty or own email | own data | own data         |
| another email      | `403`    | that user's data |
| `*`                | `403`    | everyone's data  |

`GET /api/status` is for every caller: `inference.state` is `ok` or `paused`, `inference.reasons` says why
(`storage_unavailable`, `upstream_unavailable`, `global_budget_exhausted`,
`own_budget_exhausted`, `gitlab_status_unconfirmed`; global reasons carry no amounts), `models` lists the caller's
tier models. A check that fails or hangs (each is bounded to 3 seconds) becomes a reason with `200`; when PostgreSQL
is down before the credential can be checked, authentication answers `503` like on every other route.
`alerts` lists the open critical incidents for the banner. Administrators get every one with its full message;
everyone else gets fixed, amount-free texts for an allowlist only: OpenRouter unreachable, the
shared budget and their own budget exhausted (for example "The shared 5-hour budget is exhausted; requests are
paused until it resets").

## Dashboard additions

- `GET /api/stats?group_by=user` (administrators only) adds `stats.series`: the bucket series of the eight users
  with the highest confirmed cost, plus a series with `user: ""` summing everyone else when more than eight users
  had attempts. Percentiles and cache shares of that last series are unknown, because they do not add up.
- `GET /api/usage?group=model` groups the window's totals by model; each group's `key` is the model name. `group`
  also accepts `user` and `session`, and scoping follows the rule above.
- Every model in `GET /api/models`, `GET /api/status` and `GET /api/system` carries `price_input_per_mtok` and
  `price_output_per_mtok`: USD per one million tokens as exact decimal strings (for example `"0.3"` for OpenRouter's
  `0.0000003` per token), or `null` when the catalog has no usable price.
- `GET /api/budget` returns `warn_at_percent`, the configured usage thresholds in ascending order, and each window's
  `threshold`, the highest one it has reached.
- `GET /api/budget` accepts `?user=`: administrators may read another user's windows (the global windows stay
  included); other callers get `403` for any value but their own email, and `*` is `400`.
- `POST /api/keys/revoke` revokes every active device key of the caller, or of `?user=` for administrators, and
  answers `{"revoked": n}`. Sessions are untouched, `*` is `400`, and another user for a non-administrator is `403`.
  The owner's dashboards get a `keys` event only when `n > 0`.
- `GET /api/events` is a Server-Sent Events stream that tells the dashboard what to refetch. Each event is named by
  its topic and carries no data (`event: usage` then an empty `data:` line); a client re-reads through the API it
  is authorized for. An idle stream sends a `: keep-alive` comment every 25 seconds. Who receives a topic:

  | Topic                                 | Receivers                             |
  | ------------------------------------- | ------------------------------------- |
  | `status`                              | everyone                              |
  | `alerts`, `users`                     | administrators                        |
  | `usage`, `budget`, `sessions`, `keys` | the user concerned and administrators |

  The dashboard reopens a stream the browser closed (for example after a `502` while Wrench restarts), with backoff
  from 1 to 30 seconds, and refetches everything once it is open again.

## Sessions

`GET /api/sessions` lists sessions, most recently seen first, with `?limit=` and `?user=`. `?status=active|offline`
keeps only sessions with that live status (any other value is `400`); `offline` means no request arrived within
`WRENCH_SESSION_OFFLINE_AFTER`. Each session carries `agent`, the leading product token of the latest
`User-Agent` that had one (for example `opencode/2.3.4`, at most 64 characters), or an empty string when unknown.
It is client-supplied display text, not an identity.

## OpenCode Console

Tag `opencode` is the contract a vanilla opencode reads after its device login: `/api/user`, `/api/orgs`,
`/api/v2/config` (the `wrench` provider and policies), `/api/v2/instructions`, `/api/v2/skills` and
`/api/policies/current-budgets` (budget windows for `/usage`; shared windows only for administrators). The device
login answers errors as RFC 8628 objects (`{"error": "authorization_pending"}`), not in the format below. See
[agent/opencode](../agent/opencode/README.md) and the [design](specs/06-opencode-console.md).

## Errors

Every error is JSON:

```json
{"error": {"message": "limit must be 1..1000", "type": "authentication_error", "code": "budget_exhausted"}}
```

`message` is always present; `type` appears on authentication errors and `code` on refusals with a machine-readable reason.
Amounts are decimal strings in the budget currency, never floating-point numbers, and a `null` count or time means
"not reported", never zero.

## Outside the document

| Route                                                                                         | Notes                                                                                                                                                                     |
| --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `POST /v1/chat/completions`                                                                   | OpenAI-compatible inference; device key, fresh GitLab status and a session in `X-Wrench-Session-Id` or opencode's `X-Opencode-Session-Id`; optional `X-Wrench-Request-Id` |
| `GET /auth/gitlab/login` (`?return=<path>`), `GET /auth/gitlab/callback`, `POST /auth/logout` | GitLab sign-in for the dashboard, returning to a same-origin path; end the dashboard session                                                                              |
| `GET /zen/go/v1/usage`                                                                        | `403`: opencode's OpenCode Go allowance does not exist behind Wrench                                                                                                      |
| `GET /`                                                                                       | The embedded dashboard                                                                                                                                                    |

The proxy forwards an allowlist of request fields, applies the catalog model, output ceiling and reasoning effort,
asks OpenRouter for usage with `allow_fallbacks: false`, and sets zero retries.
Prompt-cache fields (`cache_control` on messages and at the top level, `prompt_cache_key`, `prompt_cache_options`)
pass through, and the client session becomes OpenRouter's `session_id` so a conversation sticks to the provider
that holds its cache; a client-sent `session_id` is ignored. Anthropic models (`anthropic/*`) cache only at
explicit breakpoints, so Wrench adds a top-level `cache_control: {"type": "ephemeral"}` unless the client sent one.
Other models rely on the provider's automatic caching.
It persists a pending attempt before forwarding and relays the response bytes unchanged.
Missing usage stays null and missing cost stays `pending`. Only the caller's tier models are admitted.
