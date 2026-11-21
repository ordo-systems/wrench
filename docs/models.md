# Models and tiers

Which models exist, who may use them, and how requests reach OpenRouter.

## Models and tiers

`models` maps a name to its OpenRouter `upstream_id` and optional `reasoning_effort`. `tiers` decide access: each tier
lists the models its users may select, with exactly one `default`. Exactly one tier is `default: true`;
`users[].tier` overrides it. The server enforces the tier on every request, independently of client metadata: a
model outside the tier is `403`. A client's subagents use the same tier models.

| Tier      | Models                                                                               |
| --------- | ------------------------------------------------------------------------------------ |
| `basic`   | `glm-5.3` (default), `glm-5.3-flash`, `qwen3-coder`, `deepseek-v4-flash`             |
| `premium` | `opus-5.5` (default), `glm-5.3`, `glm-5.3-flash`, `qwen3-coder`, `deepseek-v4-flash` |

These assignments are initial choices, not a measured quality ranking.

## Execution

- One logical request is at most one upstream attempt. Wrench never retries, never substitutes another model when a
  request fails or a budget is exhausted, and asks OpenRouter for `allow_fallbacks: false` and
  `require_parameters: true`. OpenRouter still picks a compatible provider for the model.
- Exactly-once execution inside a provider is not promised.
- Reasoning effort: `low` for Opus and both GLM models; none for Qwen3 Coder, which advertises no reasoning parameter;
  `none` for DeepSeek V4 Flash, which advertises `none`, `high` and `xhigh` but not `low`.
- Reasoning and tool-call state stays in the conversation the client sends back; Wrench relays response bytes
  unchanged.

## Catalog

Token limits come from the OpenRouter model catalog (`context_length`, `top_provider.max_completion_tokens`; a missing
completion cap means the context length), fetched from `<WRENCH_OPENROUTER_URL>/models` at startup, hourly and after
a configuration change, and cached in PostgreSQL. The proxy clamps `max_tokens` to the completion cap; the context
length is published to clients but not enforced by the server.

A configured model is `ok`, `unknown_upstream` (the catalog lacks its `upstream_id`) or `no_catalog` (no catalog was
ever fetched). Only `ok` models are listed to clients, and requests to others get `503`. Unavailable models are
logged after each check, raise the `models:catalog` alert, and show on the Alerts tab.

The catalog also stores OpenRouter's `pricing.prompt` and `pricing.completion` (USD per token). The API publishes
them as exact dollars per one million tokens, `null` when absent or unparseable (the latter is logged as a warning).
Both Models cards of the dashboard show them as "Price in" and "Price out" (`$0.30/1M tokens`, `—` when unknown)
beside the context window (`128K`, `1M`). Prices are informational; budgets use OpenRouter's reported cost.

The five models' live compatibility through opencode → Wrench → OpenRouter is checked by the
[pilot live check](operations.md#pilot).
