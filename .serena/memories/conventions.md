# Conventions

- English for code, comments, logs, errors, developer docs and memories; Cyrillic test fixtures via Unicode escapes.
- Comments explain purpose, contract or constraint of significant types/functions; no name-restating comments. Every package has a doc comment.
- Optional provider data uses pointers (`*int64`, `*money.Amount`, `*time.Time`): nil = not reported.
- Money is `money.Amount`; never float64. YAML decodes amounts from literal text.
- Secrets use `env.Secret`; never print values in diagnostics or reports.
- Config enums are string types with consts (`config.Role`, `store.Outcome`, `store.CostState`).
- Stdlib `net/http` routing; small consumer-side interfaces (e.g. `proxy.Journal`) instead of passing the concrete store.
- Keep config changes consistent across Go model, validation, API, frontend types, examples and tests.
- Internal plans/reports go to ignored `docs-reports/`; public docs only README, AGENTS, CONTRIBUTING, SECURITY, LICENSE.
- Tests use synthetic fixtures and a controllable clock; real API calls only in separate cost-bounded compatibility checks.
- Tests live in the package they test (`package foo`, never `foo_test`); do not export symbols only for tests, set unexported fields such as `now` directly.
