# Unsettled costs

A finished request whose cost is still unknown (a stream cut before the usage chunk, a client disconnect, an upstream
error without a generation id, a crash) never pauses inference. A few cents of possible overrun are not worth
stopping the team or making an administrator investigate attempts by hand. Unknown costs are a visible hint next to
the budgets they may affect.

## Goals

- Admission ignores unknown costs: they count as zero until reconciled, so a budget may be overrun by requests whose
  cost was unknown when the next request was admitted.
- Every budget window shown in the dashboard carries a mark when it has relevant unsettled attempts: finished
  attempts with an unknown cost that started in that window — the user's own for a user window, everyone's for a
  global window. The tooltip states the count.

## Non-goals and anti-goals

- No estimating unknown costs (for example from `max_tokens`).
- Reconciliation is unchanged: it settles unknown costs from OpenRouter generation metadata.
- Nothing makes the administrator act on unsettled attempts: there is no manual settlement, no cost journal view and
  no unsettled-cost alert. The mark disappears by itself once reconciliation settles the cost or the window resets.

## Approaches

1. **Per-window count from the server (chosen).** `BudgetWindow.unsettled` is computed with spend in the store;
   `/api/budget` and `/api/users` return it, the frontend shows the mark when it is above zero.
2. One global flag: marks every budget for any user's cut stream, so it does not show relevance.
3. Counting in the frontend from the attempt list: duplicates the window boundaries in TypeScript and needs the
   attempt list on every budget view.

## Decisions

- `UnsettledMark` (Lucide `Hourglass`) appears beside a window in the budget widgets and the Users table.
- Normal in-flight attempts are not unsettled; attempts still open after `WRENCH_UPSTREAM_TIMEOUT_TOTAL` plus one
  minute are closed as interrupted and can still be reconciled by their generation id.
- An attempt without a generation id keeps an unknown cost; it marks the windows it started in until they reset.
- Every cost statement of reconciliation is appended to `charge_events`; corrections keep the previous value.
- `GET /api/requests` keeps listing attempts; the e2e tests and the live check read the journal through it. The live
  check waits for every attempt of its session to get a confirmed cost.
