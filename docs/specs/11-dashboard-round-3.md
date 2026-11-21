# Dashboard layout

Layout rules of the [dashboard](08-dashboard-redesign.md) cards.

## Decisions

- Overview: a 2 × 2 grid of halves, [Status, Shared budget] and [Models, Tiers]; the system's usage cards below.
- Usage: Models (two thirds) and Your budget (one third); the usage cards below.
- Cards in a row fill it and share the tallest card's height (CSS grid with `align-items: stretch`). Below 900 px
  they stack.
- Usage cards and detail facts sit in a grid of equal cards across the full width.
- Budget widgets count down to each window's reset ("resets in 2h 30m"), with the exact UTC time in a tooltip,
  refreshed every minute.
- Keys: Revoke sits in a fixed-width last column as a compact button, so rows keep one height.
- Dialogs (confirmation, alert details, new device key) use the native `<dialog>`, close on Escape, and put the
  primary action last.
