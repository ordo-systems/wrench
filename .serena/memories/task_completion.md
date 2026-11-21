# Task completion

1. `make lint` (includes `check-generated`; API changes start in `openapi.yaml`, then `make generate`).
2. `make test` (server DB tests must run, not skip; includes dashboard tests); `make test-race` when touching
   concurrency (enables CGO deliberately).
3. Frontend: `make web` (production build).
4. Proxy, upstream or compose changes: `make e2e`.
5. Quality tools (outside repo): `make crap` (CRAP < 10 for production functions), `make dry`,
   `make mutate MUTATE_FILES="..."`.
   - crapper scores per-package coverage; do not use `-coverpkg` (its merged profile breaks the scores).
     `storetest.Open` and `pgtest.Database` show 0% by design.
   - mutator cannot map Go import-path coverage keys to files, so the Makefile passes `--no-coverage`.
6. Short verification log in `docs-reports/`.
7. Docs-only edits: check links, prove executable tokens unchanged.
Report unavailable tools and validation limits explicitly (e.g. live OpenRouter not exercised).
