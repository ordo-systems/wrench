# Server configuration format

Part of [server requirements](01-server.md): the `config.yaml` format with a model map, tiers and nested alerts.
The file reloads on change ([reload](01-server-reload.md)); budget windows include the calendar
[month](01-server-month.md).

## Decisions

- The file is `config.yaml`. `--config` defaults to `$WRENCH_CONFIG`, then `config.yaml`; the image ships
  `/etc/wrench/config.yaml`. There is no `schema_version`.
- A model's token limits and prices come from the OpenRouter model catalog, not from the file.
- Unknown and duplicate keys are rejected; YAML strings are never interpolated or evaluated.

## Format

```yaml
models:
  <name>:
    upstream_id: anthropic/claude-opus-5.5 # required
    reasoning_effort: low # optional; omitted = parameter not sent; "none" is sent as is

budget:
  currency: USD
  timezone: UTC
  global_limits:
    5_hours: 8.00
    day: 16.00
    week: 80.00
    month: 200.00
    warn_at_percent: [80, 95]

tiers:
  <name>:
    default: true # exactly one tier
    budget: { 5_hours: 0.50, day: 1.00, week: 5.00, month: 20.00 } # every window required
    models: # at least one; exactly one default
      - name: glm-5.3
        default: true

users:
  - email: user@example.com
    role: admin # optional, default user
    tier: premium # optional, default the default tier

alerts: # optional; omitted = alerts disabled
  window: 15m
  repeat_after: 1h
  low_balance: { threshold: 5.00 } # USD
  api_errors: { threshold: 20, min_attempts: 5 } # percent of finished requests; min_attempts default 5
  llm_first_token: { threshold_p95: 30s, min_attempts: 5 } # min_attempts default 5
```

An omitted alert subsection disables that alert only.

## Validation

All errors are reported together.

- Every model name used by a tier exists in `models`.
- Exactly one tier has `default: true`; each tier has exactly one default model.
- Every `users[].tier` exists; emails are unique after normalization (trimmed, lowercased); roles are `user` or
  `admin`.
- Budget amounts are exact decimals, non-negative; zero means exhausted, not unlimited. `warn_at_percent` values are
  1..99 and increasing.
- Alert durations are positive; thresholds are non-negative; `min_attempts` is at least 1.
- A model no tier references is a startup warning, not an error.

## Access by tier

A user's tier decides what they may call; a change applies to the next request.

- `GET /api/models` lists the tier's available models (see below) with the tier's default.
- The proxy accepts a request only for a model in the caller's tier; any other configured model is 403. A name that
  is not in `models` at all is 400, like any malformed request. A model that is not available is 503.
- A user's budget limits are their tier's `budget`.

## Model catalog

- Wrench fetches `<WRENCH_OPENROUTER_URL>/models` at startup, hourly and after a configuration change. From each
  entry it keeps `id`, `context_length`, `top_provider.max_completion_tokens` (null means `context_length`),
  `pricing.prompt` and `pricing.completion`.
- Each successful fetch replaces the table `openrouter_models`. At startup Wrench loads the table before the first
  fetch. A fetch with no usable entries counts as a failure, so a degraded upstream never wipes the cache.
- A configured model is `ok` when its `upstream_id` is in the catalog, `unknown_upstream` when a catalog exists
  without it, and `no_catalog` when neither a fetch nor the table produced a catalog. Only `ok` models are available.
- `input_token_limit` in the client catalog is `context_length`; the proxy clamps `max_tokens` to
  `max_completion_tokens`.
- After every check with a model that is not `ok`, Wrench logs an error listing them. With alerts enabled, the
  `models:catalog` alert fires while any model is not `ok` or the last successful fetch is older than 24 hours.
- Prices are published as exact dollars per one million tokens (`price_input_per_mtok`, `price_output_per_mtok`),
  `null` when absent or unparseable. They are informational: budgets use OpenRouter's reported cost.
- `GET /api/system` gives administrators `models: [{name, upstream_id, status, ...}]` and the tiers.
- The LLM mock serves `GET /api/v1/models` with every model of the demo configuration and their prices.
