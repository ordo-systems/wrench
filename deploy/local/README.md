# Local

Wrench with the real GitLab and OpenRouter on one machine or a closed network, over plain HTTP: PostgreSQL
and Wrench on `http://127.0.0.1:8080`. No TLS, no Caddy, no backups; use [production](../production/README.md) for
a lasting installation.

## Prepare

1. Copy the config files (the copied files are ignored by Git):

   ```sh
   cp config/config.yaml.example config/config.yaml
   cp env.example .env
   ```

2. Register a GitLab OAuth application with:

   - scopes `openid profile email`,
   - the callback `http://127.0.0.1:8080/auth/gitlab/callback`
   - and a confidential client;

3. Create a `read_api` GitLab token for account checks.

4. Edit the `.env` file with the appropriate values:

  - `WRENCH_OPENROUTER_KEY` is your OpenRouter API key (set a credit limit on it in OpenRouter);
  - `WRENCH_POSTGRES_DSN` must include the correct `POSTGRES_PASSWORD`;
  - all placeholders in the `.env` file must be replaced with actual values;

5. Edit `config/config.yaml` file with the appropriate values:
   
   - your users (allowlisted GitLab emails - see the `users` section);
   - tiers (`tiers` section);
   - budgets (`budget` section);
   - and models (`models` section).

## Start

```sh
docker compose up -d --build --wait
```

Open [http://127.0.0.1:8080] and sign in with GitLab.

## Notes

- HTTP-only mode (`WRENCH_HTTP_ONLY=true`) drops Secure from cookies and logs a warning at every start.
- `config/` is mounted at `/etc/wrench`; edits apply without a restart. `.env` changes need `docker compose up -d`.
