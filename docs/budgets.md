# Budgets

Per-user and global spending limits in fixed UTC windows, admission, and cost reconciliation.

## Windows and limits

- A user's limits are their tier's `budget`; `budget.global_limits` holds the installation-wide windows and
  `warn_at_percent`. All four windows are required in both. User and global limits are checked together; global
  limits are independent, not a sum of user limits.
- Amounts are exact decimals parsed from the YAML text, never binary floats; quoted decimals are accepted. Negative
  and non-finite values are rejected. Zero is an exhausted budget, not unlimited access.
- `5_hours`, `day` and `week` are durations of 5, 24 and 168 hours from the fixed anchor `2026-11-02T00:00:00Z`, a
  Monday: daily windows start at midnight UTC, weekly windows on Monday midnight, and five-hour windows continue
  across midnight (at 2026-11-03 00:00 UTC the current one runs from 2026-11-02 20:00 to 01:00). `month` is the
  calendar month in UTC.
- Windows are half-open `[start, end)`. Unused allowance expires; there is no carry-over or rolling window.
- Window bounds derive from server time; no history is deleted and no reset job runs. Changing a limit never resets
  spend.

## Admission

- An attempt is persisted with its server-side start time before forwarding; the start time decides its windows.
- Before forwarding, Wrench sums confirmed costs of attempts that started in the current 5-hour, day, week and month
  windows, for the user and globally. Any spent ≥ limit refuses with `402` and code `budget_exhausted`; the message
  names the window, spend, limit and reset time. Global spend and limits are shown to administrators only: other
  users get `shared <window> budget exhausted; resets at <time>`.
- Nothing is reserved, so concurrent admitted requests may overrun. The full charge is recorded and later requests
  are refused; an overrun is not deducted from the next window.
- Admitted requests carry `X-Wrench-Budget-Warning` once a `warn_at_percent` threshold is reached. For a global
  window only administrators see the amounts (`global day 91% ($455 of $500)`); other users see `global day 91%`.
- Accounting storage unavailable means `503`; concurrency above `WRENCH_UPSTREAM_CONCURRENCY_*` means `429`.

## Unknown costs and reconciliation

- A finished or interrupted attempt with an unknown cost refuses nobody: it counts as zero until reconciled, so a
  budget may be overrun slightly. Each budget window reports how many such attempts started in it (`unsettled` in
  `/api/budget` and `/api/users`): the user's own for a user window, everyone's for a global window. The dashboard
  marks those windows with an hourglass; nobody has to act on it.
- A request that provably never reached OpenRouter (connection refused) is recorded as free (`not_sent`).
- The reconciler reads `<WRENCH_OPENROUTER_URL>/generation` 5s, 15s, 30s, 1m, 5m and 15m after an attempt finishes,
  then hourly. It confirms unknown costs, verifies reported ones and fills the actual inference provider. Retries of
  metadata never create inference attempts.
- Every cost statement is appended to `charge_events`; corrections keep the previous value. Settlement is idempotent
  and corrects the original windows.
- Attempts still open after `WRENCH_UPSTREAM_TIMEOUT_TOTAL` plus one minute are closed as interrupted. The generation
  id is stored from the first chunk, so they can still be reconciled.
- An attempt without a generation id (for example an upstream error before generation) keeps an unknown cost; it is
  never closed as zero and marks the windows it started in until they reset.
