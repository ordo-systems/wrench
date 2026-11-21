# Production

Wrench on one node behind Caddy with HTTPS: Caddy, Wrench, PostgreSQL and the backup container. Only
Caddy publishes ports (80 and 443). The full install, backup, update, rollback and restore procedures are in the
[operations guide](../../docs/operations.md).

## Prepare

1. Point DNS for `WRENCH_DOMAIN` at the host and open ports 80 and 443.
2. `cp env.example .env` and replace every placeholder; `.env` is ignored by Git. Keep `WRENCH_BACKUP_RESTIC_PASSWORD`
   and the S3 credentials outside the node too: restoring needs them.
3. `cp config/config.yaml.example config/config.yaml` (ignored by Git) and set users, tiers, models and alerts.
4. From the repository root, `make prod-config` validates the compose file against `.env`.

## Start

```sh
export WRENCH_BUILD_VERSION=$(git describe --tags --always --dirty) WRENCH_BUILD_COMMIT=$(git rev-parse --short HEAD)
docker compose up -d --build
```

## Notes

- Only `config/` is mounted into Wrench and the backup; `.env` never reaches a container or a backup.
- Edits of `config/config.yaml` apply without a restart; an invalid edit is rejected and raises `config:invalid`.
- Keep `WRENCH_HTTP_ONLY=false`.

Released images are published as `ghcr.io/ordo-systems/wrench:<version>`; see
[Releases](../../docs/operations.md#releases) for using one instead of building on the node.
