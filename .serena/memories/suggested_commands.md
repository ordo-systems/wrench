# Suggested commands

One root `Makefile` holds every target; `CGO_ENABLED=0` is exported. `make help` lists everything.
- `make test` — Go tests incl. PostgreSQL (disposable `wrench-test-pg` on 127.0.0.1:55432 via `test-db-up`) +
  dashboard tests; `make go-test RUN=TestX PKG=./internal/x` narrows it. `make lint` — workflows, Go, web,
  `check-generated`, `check-blackbox`, `site-check`. Recipes run in `bash -euo pipefail` (works with macOS make 3.81).
- `make web` builds the dashboard; `build`, `image`, `image-smoke`, `image-scan` (Trivy; exceptions in `.trivyignore.yaml` expire), `vuln` (govulncheck +
  npm audit), `prod-config` (checks `deploy/production`).
- `make e2e` runs `deploy/demo` as project `wrench-e2e` without the seed (Wrench 127.0.0.1:18080, LLM mock :18000,
  GitLab mock :19000); tests sign in through the mock (`tests/login`). Also `livecheck`, `backup-drill`,
  `opencode-contract`.
- `make demo|demo-down|demo-reset|demo-logs` runs `deploy/demo` (seeded via `wrench mock seed`).
- golangci-lint is pinned via `go run`.
- `make cover` — profile at `target/coverage/go/coverage.out` (path crapper expects).
- DB tests read `WRENCH_TEST_POSTGRES_DSN`; helpers `internal/store/storetest.Open` (migrated store) and
  `internal/store/pgtest.Database` (bare DSN; used by package store itself and `internal/app/server` to avoid an
  import cycle).

## Darwin notes
- BSD `sed`: in-place edit needs `sed -i '' ...`. No `timeout`; use `perl -e 'alarm N; exec @ARGV' cmd`.
- Node 26 ships an experimental global `localStorage` that shadows jsdom's; dashboard tests stub it.
