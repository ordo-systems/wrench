# Dashboard

The dashboard is a single-page app with one tab per job and live data, in the look of the
[keen-proxy demo](https://ordo-systems.github.io/keen-proxy/demo/). There is no Refresh button: every widget and table
updates itself. Tables and filters are in [tables and filters](09-dashboard-polish.md), prices, activity grids and
the top-bar status in [models and activity](10-dashboard-round-2.md), layout details in
[layout](11-dashboard-round-3.md); the role boundary is in [dashboard roles](05-dashboard-roles.md).

## Goals

- Users see their own usage, budget, sessions and keys; administrators also see the whole system, users and alerts.
- Data stays current without a refresh button: the server announces changes over SSE and the page reloads only what
  changed.
- Every usage card links to a detailed view; administrators can split any metric by user.

## Non-goals and anti-goals

- No second data API: SSE carries change signals, not data.
- No request journal in the dashboard; `GET /api/requests` stays for tests and the live check.
- The dashboard never writes configuration.
- A user must never see another user's data, through tables, SSE events or the alert banner.

## Approaches

- **SSE as change signals (chosen).** `GET /api/events` sends topic names; the page refetches the REST endpoints it
  already uses. Authorization stays in the REST handlers; an event carries no data to leak. Costs one GET per change,
  damped by a debounce.
- **SSE carrying data.** Rejected: the server would track each subscriber's view and rebuild authorized payloads, a
  second API beside REST with its own role checks.
- **Server ticks, page refetches everything.** Rejected: polling in SSE clothing, late and loading the server while
  nothing changes.

## Navigation

- Vue Router with HTML5 history. `internal/web` serves `index.html` for unknown extension-less paths; `/api`, `/v1`,
  `/auth`, `/zen`, `/healthz` and `/readyz` keep precedence in the Go mux.
- Routes and access:

  | Path                                  | Tab                                               | Access                                                   |
  | ------------------------------------- | ------------------------------------------------- | -------------------------------------------------------- |
  | `/overview`                           | Overview                                          | admin                                                    |
  | `/usage`                              | Usage                                             | everyone                                                 |
  | `/sessions`                           | Sessions                                          | everyone; users see their own, admins pick a user or all |
  | `/keys`                               | Keys                                              | everyone; users see their own, admins pick a user or all |
  | `/users`                              | Users                                             | admin                                                    |
  | `/alerts`                             | Alerts                                            | admin; the tab shows the count of open alerts            |
  | `/overview/:metric`, `/usage/:metric` | detail of one usage card                          | as the parent tab                                        |
  | `/device`                             | device approval page, without the dashboard shell | signed-in users                                          |

- `/` and unknown paths go to `/overview` for administrators and `/usage` for users. A user opening an admin route
  lands on `/usage`. Tabs a role cannot open are not shown.
- `:metric` is one of `spend`, `tokens`, `attempts`, `first-token`, `cache`, `sessions`. The period travels in the
  query (`?period=24h`), so a detail view is a shareable link.

## Look

- Theme tokens from the reference: dark by default (`--page #141b1c`, `--card #1b2425`, `--line #35433f`,
  `--text #e8eeeb`, `--muted #a8b4b0`, `--accent`/`--ok #b2eb82`, `--warn #e4ae5d`, `--bad #f28e8e`); light under
  `:root[data-theme=light]` (`--page #f6f8f4`, `--card #fff`, `--line #cdd7ce`, `--text #202d27`, `--muted #5b685f`,
  `--accent`/`--ok #35652b`, `--warn #97600b`, `--bad #b32c35`). A theme toggle in the top bar, remembered in
  `localStorage`; the first visit follows `prefers-color-scheme`. Charts take `--chart-1..8` and the status tokens.
- Inter for text, `ui-monospace` for ids, keys and numbers. A top bar (logo and brand, tabs with a 4 px accent
  underline on the active one, the status indicator, the signed-in email and role, theme toggle), content up to
  1600 px wide.
- Cards: `--card` background, 1 px `--line` border, 10 px radius, 14–16 px padding, no shadow. Status pills: 999 px
  radius, 1 px border in the status color. Icons: Lucide (`lucide-vue-next`).

## Widgets and tabs

| Widget        | Shows                                                                                        | Source                                                       |
| ------------- | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| Status        | admin: `postgres`/`openrouter` checks, pause reasons, config revision and error, backup      | `GET /api/status`, `GET /api/system`                         |
| Models        | user: the tier's models with context, prices and today's use; admin: every configured model  | `GET /api/models`, `GET /api/usage`; admin `GET /api/system` |
| Tiers         | admin: each tier with its models, default model, default-tier mark and budget                | `GET /api/system`                                            |
| Your budget   | one bar per window (5 h, day, week, month): spent of limit, threshold marks, reset countdown | `GET /api/budget`                                            |
| Shared budget | the same for the global windows                                                              | `GET /api/budget` as admin                                   |
| Usage cards   | six cards with the period's value and an activity grid; each opens its detail view           | `GET /api/stats`                                             |

- Overview: [Status, Shared budget], [Models, Tiers], then the system's usage cards.
- Usage: [Models, Your budget], then the usage cards. Administrators pick whose budget and usage are shown.
- Cards: Confirmed spend, Tokens (input + output; reasoning in the detail), LLM requests (attempts by outcome),
  Time to first token (p95; p50/p95 lines in the detail), Cached share of input, Active sessions (from requests in
  5-minute steps).
- Period: 24 h (`hour` buckets), 7 d (`hour`), 30 d (`day`); default 24 h, kept in the query. A Model filter sits
  beside it.
- Detail view: the full chart of the metric for the period, then its facts; for administrators with all users a
  "by user" toggle. Additive metrics (spend, tokens, requests, sessions) stack the top 8 users plus "others";
  first-token percentiles and cached share draw one line per top-8 user, without "others", since percentiles and
  ratios do not add up.
- Missing telemetry is a gap with a note on how many attempts reported it, never zero; costs are confirmed costs,
  with the number of unknown ones beside them.

## Per-user series

`GET /api/stats?group_by=user` (admin only; 403 otherwise) adds `series`: `{user, buckets}` for the 8 users with the
highest confirmed cost in the period plus one entry `user: ""` for the rest when more than eight users had attempts.
The `""` entry carries only additive fields; its percentile and cache-share fields are null.

## Live updates

- `GET /api/events` (authenticated, `text/event-stream`): `event: <topic>` with an empty `data`, topics `usage`,
  `sessions`, `keys`, `users`, `alerts`, `status`, `budget`. A `: keep-alive` comment every 25 s keeps proxies from
  closing the stream.
- An in-process bus: a finished attempt publishes `usage` and `budget` for its user and `status` for everyone
  (another user's spend can exhaust the global budget); a reconciled cost publishes the same; session touches publish
  `sessions`; key changes `keys` (bulk revocation only when it revoked something); user revocation `users` and
  `keys`; the alerts runner `alerts` and `status` when incidents change; config reload and the upstream probe
  `status`.
- Delivery: an event about one user reaches that user and administrators; `alerts` and `users` reach
  administrators; `status` reaches everyone. A slow subscriber loses events rather than blocking publishers.
- The page refetches the widgets bound to a topic, at most once per second per topic. It reopens a stream the
  browser closed with backoff from 1 to 30 seconds and refetches everything once it is open again; sign-out stops
  the reopen timer.
- Caddy streams unbuffered (`flush_interval -1`); the server's write deadline stays unset.

## Critical alert banner

- Every signed-in user sees a banner under the top bar while a critical incident they may see is open.
  Administrators see every critical incident with its message and a link to `/alerts`.
- Users see an allowlist with fixed texts that carry no amounts: OpenRouter unreachable, the shared budget exhausted,
  their own budget exhausted with its window. Everything else is admin-only.
- Source: `alerts` of `GET /api/status`, filtered and worded on the server, refreshed on `status` events.

## Frontend stack

Vue 3, `vue-router` 4, `echarts` 5 with `vue-echarts` 7 (tree-shaken), `lucide-vue-next`; exact versions pinned in
`package.json`. `api.ts` and the generated types are the data layer; `live.ts` owns the EventSource and the
topic → refetch wiring; chart data comes from the pure, tested `metrics.ts`.
