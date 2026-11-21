# Server image

Part of [server requirements](01-server.md): one distroless, non-root image runs the server, the backups and the
mocks.

## Decisions

- One root `Dockerfile`; the build context is the repository root. Services differ only in `command`.
- Base `gcr.io/distroless/cc-debian12:nonroot` (glibc). `pg_dump` and `pg_restore` are dynamically linked against
  `libpq` and its dependencies, so they cannot run on `distroless/static`.
- `pg_dump` and `pg_restore` come from `postgres:17-bookworm` (Debian 12, the same glibc) together with every shared
  library `ldd` reports for them. The PostgreSQL major stays pinned to the server's (17).
- The image runs as `nonroot` (UID/GID 65532).
- Every base image is pinned by digest.

## Dockerfile stages

| Stage     | From                                    | Produces                                                          |
| --------- | --------------------------------------- | ----------------------------------------------------------------- |
| `web`     | `node:22.19.0-alpine`                   | dashboard `dist/`                                                 |
| `build`   | `golang:1.27.4-alpine`                  | `wrench` with version metadata; empty runtime directories         |
| `pgtools` | `postgres:17-bookworm`                  | `pg_dump`, `pg_restore` and their libraries under their own paths |
| `restic`  | `restic/restic:0.20.0`                  | static `restic`                                                   |
| final     | `gcr.io/distroless/cc-debian12:nonroot` | everything above                                                  |

`web` and `build` run on `--platform=$BUILDPLATFORM` and cross-compile with `GOOS=$TARGETOS GOARCH=$TARGETARCH`.
`pgtools` runs on the target platform and copies each binary and each library from its `ldd` output into a staging
root, keeping absolute paths, so the same Dockerfile builds on amd64 and arm64.

The final image contains:

- `/usr/local/bin/wrench`, `/usr/local/bin/restic`, `/usr/local/bin/pg_dump`, `/usr/local/bin/pg_restore` and the
  copied libraries;
- `/etc/wrench/config.yaml` (the reference configuration);
- `/var/lib/wrench-backup/restic`, `/var/lib/wrench-backup/status` and `/restore`, owned by 65532, so named volumes
  mounted there start writable. Distroless has no shell, so the build stage creates them.

`ENV WRENCH_CONFIG=/etc/wrench/config.yaml`, `EXPOSE 8080`, `USER 65532:65532`,
`ENTRYPOINT ["/usr/local/bin/wrench"]`, `CMD ["run", "server"]`.

## Compose

Each compose file shares one build through a YAML anchor and one image name (`wrench:${WRENCH_VERSION:-local}` in
production). `backup` runs `["run", "backup"]`; the mocks run `["mock", ...]`.

## Make targets

- `make image` builds `wrench:dev`.
- `make image-smoke` runs the built image with `--entrypoint` overrides and fails unless `pg_dump --version` and
  `pg_restore --version` report PostgreSQL 17, `restic version` reports 0.20.0, and `wrench version` prints the
  build version.
- `make image-scan` scans `wrench:dev` (or `SCAN_IMAGE`) with Trivy; see [CI and release](04-ci-release.md).
