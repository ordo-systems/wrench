# Server CLI

Part of [server requirements](01-server.md): one `wrench` binary for the server, the backups and the mocks.

## Decisions

- One image, separate containers: compose runs `wrench run server` and `wrench run backup` as different services,
  so backup credentials never reach the internet-facing server process.
- The CLI is built with [cobra](https://github.com/spf13/cobra). Flags use the POSIX form (`--config`).

## Layout

- `cmd/wrench` — the cobra command tree, one `newXxxCmd` constructor per command. `main.go` wires signals and maps
  errors to exit codes; each command parses its flags and calls an application package directly.
- `internal/app/server` — validate configuration, migrate, serve, graceful shutdown.
- `internal/app/backup` — scheduler loop, one-off backup, restore.
- `internal/app/mocks` — the mocks and the demo seed.
- `internal/version` — build metadata set through `-ldflags -X`.

## Commands

| Command                                                    | Purpose                                                                            |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `run server [--config path]`                               | Serve the API, proxy and dashboard.                                                |
| `run backup`                                               | Run backups on `WRENCH_BACKUP_SCHEDULE` until stopped.                             |
| `backup`                                                   | Run one backup and exit.                                                           |
| `restore [--from s3\|local] [--config-out dir] [snapshot]` | Restore a snapshot (default `latest`) and exit.                                    |
| `mock llm`, `mock gitlab`, `mock seed`                     | [Server mocks](01-server-mocks.md) and [demo data](01-server-deploy.md#demo-data). |
| `healthcheck <url>`                                        | Probe a URL and exit; container health checks have no shell or curl.               |
| `version`                                                  | Print the version.                                                                 |

Defaults: `--config` falls back to `WRENCH_CONFIG`, then `config.yaml`; `--from s3`, `--config-out /restore/config`.

## Version

- `internal/version` holds `Version` and `Commit`, defaulting to `dev` and `unknown`.
- The Makefile and Dockerfile pass `git describe --tags --always --dirty` and the short commit hash through
  `-ldflags -X`. Docker builds receive them as build arguments because `.git` is not in the build context.
- `version` prints `wrench <version> (<commit>)`. The server logs both at startup and reports them in
  `GET /api/system`.

## Errors and exit codes

- Usage errors (unknown command or flag, wrong argument count) print usage and exit 2.
- Runtime errors print `wrench: <error>` and exit 1.
- SIGINT and SIGTERM cancel the command context; the server and the backup loop shut down gracefully.
