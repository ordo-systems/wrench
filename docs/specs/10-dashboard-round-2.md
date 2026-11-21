# Dashboard models, activity and status

How the [dashboard](08-dashboard-redesign.md) shows model prices and per-model usage, activity grids, the top-bar
status, buttons and the device-key how-to.

## Goals

- Users see which models they may use, what they cost and how much they used each today.
- Usage cards read at a glance as activity grids, with the period stated on every card.
- Health is always visible in the top bar.

## Non-goals and anti-goals

- No new chart library; detail views keep ECharts.
- Prices are informational: budgets keep using OpenRouter's reported cost.
- A user never sees another user's data.

## Approaches

- **Activity grid in CSS (chosen).** A strip of cells whose color steps with the value; no chart code in the cards,
  cheap to render, readable in both themes.
- **ECharts heatmap.** Rejected: pulls the heatmap and visualMap modules into the bundle for small cards that need no
  axes or zoom.
- **Prices from config.** Rejected: OpenRouter's catalog already carries them and stays current.

## Backend

- The [model catalog](01-server-config.md#model-catalog) stores OpenRouter `pricing.prompt` and
  `pricing.completion` (USD per token, verbatim text, empty when absent) in `openrouter_models`.
- Every model in `GET /api/models`, `GET /api/status` and `GET /api/system` carries `price_input_per_mtok` and
  `price_output_per_mtok`: exact decimal strings of dollars per one million tokens (`"0.3"` for `0.0000003` per
  token), `null` when absent or unparseable (the latter logged as a warning).
- `GET /api/usage?group=model` groups the window's totals by model; each group's `key` is the model name. `group`
  also accepts `user` and `session`; scoping follows the [roles](05-dashboard-roles.md).

## Models card

- Columns: Model, Context (compact: `128K`, `1M`), Price in, Price out (each price followed by a small faint unit,
  `$0.30/1M tokens`; an unknown price is `—` alone).
- User: the caller's tier models, plus Requests today and Spend today (from `/api/usage?window=day&group=model`),
  the spend with a horizontal bar against the day's largest model spend.
- Admin: every configured model with its catalog status; route errors are on Alerts.

## Usage cards

- Title left, the period right in faint short form (`24h`, `7d`, `30d`), the value large, monospace and
  right-aligned.
- The activity grid is one strip of cells in UTC like the bucket starts: the last 24 hours, the last 168 hours or the
  last 30 days. Each cell shades `--accent` in four steps of the period's maximum (empty and zero cells use `--line`)
  and titles its time and value. The whole card stays one link to the detail view.
- Detail view facts: a full-width grid of equal cards with a title, the value (large, monospace, accent) and a faint
  subtitle such as "reported by 38 of 39".

## Top-bar status

- An icon left of the email: Running (`CircleCheck`, inference runs and every check is ok), Degraded
  (`TriangleAlert`, a system check is not ok; administrators only), Paused (`CircleX`) or Unknown (`CircleHelp`,
  while `/api/status` has not answered). The status is in `aria-label` and `title`.
- Clicking it opens a popover with the pause reasons and, for administrators, the checks. Refreshed on `status`
  events.

## Buttons and copy

- Three button styles across the dashboard: default (outlined), `primary` (accent fill), `danger` (bad color); hover,
  focus-visible ring and disabled states; every button carries a Lucide icon; icon-only buttons stay square.
- Selects share one style with a custom chevron.
- One copy button component: idle (Copy icon) → "Copied" (Check, 2 s) → idle; a failure shows X and "Copy failed"
  for 2 s.

## Keys

"New device key" (primary) opens a wide dialog with a key icon in the title and numbered steps: install opencode;
run `opencode auth login opencode --method device --answer server=<origin>` (the page's own origin) in a full-width
code block with Copy in a bar above it; open the shown `/device` link, check the code and approve. Close is the
primary button.
