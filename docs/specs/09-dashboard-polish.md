# Dashboard tables and filters

How the [dashboard](08-dashboard-redesign.md) presents tables, filters and revocation.

## Goals

- Every table reads at a glance: numbers in monospace and right-aligned, statuses as colored pills with icons,
  dates with a relative time, long text in a dialog on click.
- Administrators look at one user (themselves by default) or everyone on Usage, Sessions and Keys.
- Every revocation asks for confirmation first.
- The demo shows every widget and table with data.

## Non-goals and anti-goals

- No UI component library: dialogs use the native `<dialog>` element.
- A user never sees another user's data; the user filter is admin-only and the server enforces it.

## Approaches

- **Agent from the request's `User-Agent`, stored on the session (chosen).** The session touch writes on each
  request anyway; it also stores the latest product token (`opencode/2.3.4`), so the client version shows after an
  upgrade.
- **Agent from the device login.** Rejected: the device flow accepts only `opencode-cli`, so it would always read
  "opencode", and a key outlives client upgrades.
- **Client-side "revoke all my keys" as a loop of `DELETE /api/keys/{id}`.** Rejected: a broken loop leaves some
  keys live. One endpoint revokes them in one transaction.

## Common

- Numbers in tables and cards: `ui-monospace`, `font-variant-numeric: tabular-nums`, right-aligned, headers of
  numeric columns right-aligned too.
- One placeholder for empty values: `—`. Usage cards: when the period has no requests, all six show `—`; otherwise
  each shows its value, zero included (a percentile without observations stays `—`).
- Status pills with a Lucide icon and the status color: ok/active (`CircleCheck`, `--ok`), warning/offline/pending
  (`TriangleAlert` or `Clock`, `--warn`), critical/error/revoked/blocked (`CircleX`, `--bad`), resolved/expired
  (`CircleCheck`/`CircleMinus`, `--muted`).
- Dates: absolute time plus a relative one (`5 min ago`, `in 3 days`), refreshed once a minute without a refetch;
  UTC in tooltips. All UI copy is English.
- Usage cards are one link each, with hover and focus states.
- Brand: an SVG wrench in `--accent` left of "Wrench" in the top bar and on the sign-in page, linking to `/`; also
  the favicon.

## Sign-in page

A centered card: logo, "Wrench", the tagline "Private team coding agent", "Sign in with GitLab" with the tanuki icon
(inline SVG; Lucide has no brand icons), and the refusal message, if any, inside the card.

## User filter (admin)

- A select on Usage, Sessions and Keys, and in the usage detail view: "Me (<email>)" (default), "All users", then
  each user by email. The choice travels in the query as `?user=<email>` or `?user=*`; no parameter means "Me".
- Usage with one user shows that user's budget and usage cards; with "All users" it shows the system's. The by-user
  split of a detail chart is offered for all users only.
- Users never see the select; their requests send no `user`.

## Sessions

- Columns: Status pill, Session (monospace, trimmed with a copy button), User (all users), Agent, Device, Started,
  Last seen, Duration (last seen − started), Requests, Cost, Unconfirmed (hidden when zero in every row; tooltip
  "requests whose final price OpenRouter has not confirmed yet").
- Sorted by Last seen, newest first, on the server.
- Status filter: All / Active / Offline, in the query (`?status=`), applied on the server; `offline` means no
  request within `WRENCH_SESSION_OFFLINE_AFTER`.
- `Session.agent` is the text of `User-Agent` before the first space, cut to 64 characters, set on every touch; an
  empty header keeps the stored value; empty means unknown. It is display text, not an identity.

## Keys

- Each active key has Revoke in a fixed-width last column; "Revoke all" revokes every active key of the shown user
  and is hidden for "All users". Both open a confirmation dialog naming the device or the count.
- "New device key" (shown for "Me" only) opens the login how-to ([details](10-dashboard-round-2.md#keys)). The empty
  state shows the same command.
- `POST /api/keys/revoke` revokes all active keys of the caller, or of `?user=` for administrators, in one
  transaction, and returns `{revoked}`. Sessions stay. A non-administrator passing another user gets 403, `*` gets
  400. It publishes `keys` for that user when `revoked > 0`.

## Users

- Columns: User, Role, Tier, State pill (`never signed in` until the first sign-in), Active keys, Last key use, then
  5h, Day, Week, Month: each with the percent, the amount and a small bar colored by the `warn_at_percent`
  thresholds, plus the [unsettled mark](12-unsettled-without-pause.md). Then "Revoke all keys" for users who have
  signed in, with the confirmation "Revoke every key and session of <email>?", calling
  `POST /api/users/{email}/revoke`.

## Alerts

- Filters: severity (All / Critical / Warning) and status (Open / Resolved / All, default Open), kept in the URL and
  applied in the browser; the incident list is small.
- Open incidents first; a Status pill: Open (severity color) or Resolved (muted, row dimmed).
- Message trimmed to one line. Clicking a row, or Enter or Space on it, opens a dialog with severity, status, user,
  the full message, the incident key and the opened, last seen and resolved times; Escape closes it.
- The table fits the card: fixed layout, Message takes the remaining width; horizontal scroll only below 700 px.

## Demo data

The [seed](01-server-deploy.md#demo-data) writes, for every configured user, three device keys (active, revoked,
expired) and sessions bound to them with an agent (`opencode/2.3.x`), so Sessions, Keys and Users have rows for each
user.
