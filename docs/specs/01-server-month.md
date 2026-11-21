# Monthly budget window

Part of [server requirements](01-server.md): the `month` budget window in the
[configuration format](01-server-config.md).

## Decisions

- `month` is the calendar month in UTC: `[first day 00:00, first day of the next month 00:00)`. Its length varies
  (28–31 days). The other windows keep their fixed anchor `2026-11-02T00:00:00Z`.
- `month` is required wherever windows are listed: `budget.global_limits` and every `tiers.<name>.budget`. A missing
  window would silently mean "unlimited", and zero already means "exhausted".
- Spend is summed from `attempts` by window bounds, so a window needs only its bounds, never stored state.

## Behavior

- `store.Windows` lists `5_hours`, `day`, `week`, `month` in that order; `Bounds` computes the calendar month;
  `ParseWindow` accepts `month`.
- A missing window is reported as `budget window month is required`.
- Enforcement, `warn_at_percent` warnings, exhaustion messages with the reset time, alert keys and the dashboard
  follow the window list, so `month` needs no special cases.
