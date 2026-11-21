# Configuration

Which file owns which setting, how changes are applied, and the validation rules.

## Files

| File                                                                    | Purpose                                                                     |
| ----------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| [deploy/shared/config/config.yaml](../deploy/shared/config/config.yaml) | Reference configuration: models, budget, tiers, users, alerts               |
| `deploy/<environment>/config/config.yaml`                               | The deployed copy, mounted with its directory at `/etc/wrench`              |
| `deploy/<environment>/.env`                                             | Runtime settings and secrets; never print values in diagnostics             |

Deployments live in [deploy/](../deploy): [demo](../deploy/demo/README.md) (mocks, seeded data, `make demo`),
[local](../deploy/local/README.md) (real GitLab and OpenRouter over HTTP) and [production](../deploy/production/README.md)
(HTTPS, backups). Every `.env` and the local and production `config/config.yaml` are ignored by Git; start from the
`env.example` and `config/config.yaml.example` next to them. The reference configuration is used by tests and baked
into the image; environments keep their own copies. The format is described in
[the configuration design](specs/01-server-config.md); the variables in
[server requirements](specs/01-server.md#environment-variables).

## Applying changes

`config.yaml` changes apply without a restart: Wrench watches its directory (file events plus a check every
10 seconds), validates the whole candidate, and swaps it in atomically; each request uses one configuration.
An invalid edit is not applied: the previous configuration keeps serving, the error is logged, shown on the
Overview status widget and raised as the `config:invalid` alert until the file is fixed. Removed users are refused
on their next request and tier changes apply to the next request. Each environment mounts its whole `config/`
directory, so an edit reaches the container however the file is saved.

Environment (`.env`) changes take effect on a restart (`docker compose up -d`), which validates them first.
The dashboard never writes configuration, and revocation does not edit configuration files.

## Validation

- Unknown and duplicate YAML keys, duplicate normalized user emails and duplicate model names are rejected.
- YAML strings are taken literally: no shell evaluation or interpolation.
- Every error is reported at once; a failed candidate leaves the running configuration intact.
- Missing required environment variables stop startup; there is no anonymous or development fallback.
- Secrets stay out of committed files, examples and diagnostics.
