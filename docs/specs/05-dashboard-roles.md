# Dashboard roles

A user sees only their own data and whether their requests can run; an administrator additionally sees everyone's
data and the system. The boundary is enforced by the API, not only hidden in the dashboard.

## Decisions

- **Scope parameter.** `?user=` on `listRequests`, `listSessions`, `getUsage`, `getStats`, `listKeys`,
  `revokeKeys` and the admin-only `listAlerts` means:

  | Value         | user | admin     |
  | ------------- | ---- | --------- |
  | empty         | self | self      |
  | own email     | self | self      |
  | another email | 403  | that user |
  | `*`           | 403  | everyone  |

  The default is the caller for every role, so a forgotten parameter never shows other people's data.
  A non-administrator asking for someone else gets 403 instead of a silent fallback to self. `getBudget` and
  `revokeKeys` accept one email only; `*` is 400.
- **Admin-only operations.** `getSystem`, `listUsers`, `listAlerts` and `revokeUser` carry `x-wrench-admin: true`;
  the guard refuses everyone else before the handler runs, so handlers have no role branches.
- **Role-dependent fields.** `getBudget` includes the global windows for administrators only; other users learn
  about a global refusal from its 402 message, which carries no amounts, and `X-Wrench-Budget-Warning` gives them
  only the percent for a global window. `DELETE /api/keys/{id}` revokes an own key; administrators may revoke any
  key.
- **`GET /api/status`** (authenticated, every role) tells the caller whether their inference can run:
  - `inference.state`: `ok` or `paused`; `inference.reasons`: zero or more of `storage_unavailable`,
    `upstream_unavailable`, `global_budget_exhausted`, `own_budget_exhausted`, `gitlab_status_unconfirmed`.
    `paused` iff the list is not empty. Global reasons carry no amounts.
  - `models`: the caller's tier models (name, limits, reasoning effort, prices), no upstream ids or catalog status.
  - `version`.
  - `alerts`: open critical incidents for the banner. Administrators get every one with its message; other users
    get fixed, amount-free texts for an allowlist only: OpenRouter unreachable, the shared budget and their own
    budget exhausted.
  - Each check is bounded to 3 seconds; a failing or hanging check becomes a reason with `200`. When PostgreSQL is
    down before the credential can be checked, authentication answers `503` like on every other route.
- Out of scope: new roles or per-permission grants, editing users or configuration from the dashboard (the dashboard
  never writes configuration).

## Server

- `internal/httpapi/views.go`: `scopeOf`/`scopeFor` are the only place of the scope rule; they return `""` for
  everyone, an email, or `errForeignScope`. Each list handler maps the error to its 403 response.
- `internal/httpapi/status.go`: `GetStatus` gathers the inputs (storage ping, upstream probe, the caller's and the
  global budget, GitLab status freshness, the tier's catalog models) and the pure `pauseReasons` decides. A budget
  window is exhausted when `percent >= 100`. Unknown costs never pause inference
  ([unsettled costs](12-unsettled-without-pause.md)).

## Dashboard

- Tabs a role cannot open are not shown, and their routes redirect to the role's landing tab
  ([dashboard](08-dashboard-redesign.md#navigation)).
- A user's requests never pass `?user=`. An administrator picks "Me" (no parameter), "All users" (`*`) or one user
  on Usage, Sessions and Keys ([user filter](09-dashboard-polish.md#user-filter-admin)).
- Admin data (`/api/system`, `/api/users`, `/api/alerts`) is requested only for administrators.
