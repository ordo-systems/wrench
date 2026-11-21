# Development

Repository layout, commands, and what is still unverified.

## Layout and commands

Stack: a Go monolith, PostgreSQL, Vue, GitLab OIDC and OpenRouter; clients are vanilla opencode. Requirements:
Go 1.27, Node 22.19+, Docker, Python 3. `make` lists every target and tunable variable; `make doctor` checks the tools.

| Layout                                                | Contents                                                                                                       |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `go.mod`                                              | The one Go module, `github.com/ordo-systems/wrench`                                                              |
| `api/`                                                | The OpenAPI document and the generated server code                                                             |
| `cmd/wrench/`                                         | The `wrench` binary: server, backup and development mocks                                                      |
| `internal/`                                           | Server packages (cli, app/server, app/backup, config, proxy, store…)                                           |
| `webapp/`                                             | Vue dashboard; `make web` builds it into `internal/web/dist`, which the binary embeds                          |
| `Dockerfile`                                          | The single Wrench image (server, backup, mocks); build context is the repository root                          |
| `deploy/shared/`                                      | Reference `config/config.yaml`, PostgreSQL init files and the Caddyfile                                        |
| `deploy/demo/`, `deploy/local/`, `deploy/production/` | Compose, README, `env.example` and `config/` per environment                                                   |
| `agent/opencode/`                                     | opencode setup guide and the `opencode` contract test                                                          |
| `tests/`                                              | Black-box `e2e`, `livecheck`, `backup-drill` and their tools; `make check-blackbox` keeps them off `internal/` |
| `website/`                                            | GitHub Pages site: the published files in `site/`, its own `Makefile` (`serve`, `check`)                      |
| `docs/specs/`                                         | Design specifications of the current behavior                                                                  |

The root `Makefile` holds every target. Recipes run in bash with `-euo pipefail`; `make print-<VAR>` prints a variable.

| Command                     | Purpose                                                                                                                                |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `make test` / `make lint`   | Go tests (server tests use PostgreSQL on port 55432) and dashboard tests; `lint` also checks generated API code, the black-box rule and the website |
| `make go-test RUN=… PKG=…`  | Only the matching Go tests, for example `RUN=TestBudget PKG=./internal/budget`                                                         |
| `make lint-workflows`       | actionlint, SHA pins and the release tag script test for `.github/workflows` (part of `make lint`)                                     |
| `make generate`             | Regenerate API code from `api/openapi.yaml` (see below)                                                                                |
| `make e2e`                  | Demo stack with the mocks: streaming, tools, cost, cancel, sign-in                                                                     |
| `make build` / `make image` | Binary in `bin/wrench` / the `wrench:dev` image; `image-smoke` checks its tools                                                        |
| `make vuln` / `image-scan`  | govulncheck and `npm audit` (high and above) / Trivy on `wrench:dev` (HIGH and CRITICAL; exceptions in `.trivyignore.yaml`)    |
| `make demo`                 | Demo with seeded data on `localhost:18080` (GitLab mock picker); `demo-down`, `demo-reset`, `demo-logs`                                |
| `make prod-config`          | Validate production Compose against `deploy/production/.env` (or `env.example`)                                                        |
| `make livecheck`            | Pilot live check against `WRENCH_URL` with `WRENCH_TOKEN`                                                                              |
| `make backup-drill`         | Restore drill against a stand-in S3                                                                                                    |
| `make opencode-contract`    | The installed opencode (2.3.x) through device login and a run against the demo stack                                                   |
| `make site` / `site-check`  | Serve the website on `127.0.0.1:8000` / check its links, anchors and shared docs navigation                                            |
| `make crap dry mutate`      | ub-mode quality tools (installed outside the repository; `MUTATE_FILES` is required)                                                   |

## API contract

[`api/openapi.yaml`](../api/openapi.yaml) is the source of truth for the JSON API. `make generate` (needs `npm ci`
in `webapp`) writes:

| Output                                                            | Generator                                              |
| ----------------------------------------------------------------- | ------------------------------------------------------ |
| `api/api.gen.go`: models, routing and the strict server interface | oapi-codegen, pinned by a `tool` directive in `go.mod` |
| `webapp/src/api.gen.ts`: types only                               | openapi-typescript, pinned in `webapp/package.json`    |
| `tests/internal/wrenchapi/wrenchapi.gen.go`: models and client    | oapi-codegen, pinned in `go.mod`                       |

To change the API: edit the document, run `make generate`, implement the new or changed method of
`api.StrictServerInterface` in `internal/httpapi` (the compiler lists what is missing), and commit the document and
the generated files together. Generated files are never edited by hand; `make check-generated`, part of `make lint`,
fails when they differ from what the document produces. The server embeds the document and reads `security` and
`x-wrench-admin` from it to authenticate and authorize before any handler runs.

## Tests

Server tests run against a disposable PostgreSQL (`make test-db-up`); without `WRENCH_TEST_POSTGRES_DSN` they skip,
which does not count as passing. Tests use explicit fixtures (`config/configtest`, `catalog/catalogtest`,
`gitlab/gitlabtest`) and controllable clocks; there is no separate test configuration file. Internal verification
records go to the ignored `docs-reports/`.

## Known gaps

- Nothing is deployed yet. GitLab sign-in and status checks were validated against a fake GitLab, not the company
  instance.
- Live OpenRouter compatibility of the five models is unverified; the automated chain uses the LLM mock. The
  generation endpoint shape (`data.total_cost`, `data.provider_name`) was tested against the mock.
- `input_token_limit` is published to the client but not enforced by the server.
- Upstream errors without a generation id keep an unknown cost forever; it counts as zero and marks its budget
  windows until they reset. How often this happens with live OpenRouter is unmeasured.
