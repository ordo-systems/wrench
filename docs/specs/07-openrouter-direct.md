# OpenRouter upstream

Wrench calls OpenRouter itself, with `net/http`. It is bound to OpenRouter on purpose: budgets need OpenRouter's
prices (`usage.cost`, `/generation`), so a provider-adaptation layer would buy nothing and cost a container, a
second key and a re-serialized stream.

## Goals

- Inference, cost reconciliation, the model catalog and the balance alert go straight to OpenRouter.
- Clients receive OpenRouter's response bytes unchanged.
- Operators see whether OpenRouter is reachable with Wrench's key.

## Non-goals and anti-goals

- No provider abstraction and no other upstreams.
- Upstream health never restarts Wrench and never takes it out of traffic: the dashboard, sign-in, budgets and the
  Console configuration work without OpenRouter.

## Decisions

- **Upstream.** `WRENCH_OPENROUTER_URL` (default `https://openrouter.ai/api/v1`) and `WRENCH_OPENROUTER_KEY`
  (required). `chat/completions`, `generation`, `models` and `key` are joined to that base, so the
  [LLM mock](01-server-mocks.md) serves as the upstream unchanged.
- **Key handling.** The OpenRouter key lives in `wrench` only. Operators set a credit limit on it in OpenRouter.
- **Request body.** The proxy forwards an allowlist of client fields and sets the catalog model, the output ceiling,
  `reasoning_effort` (OpenRouter's shorthand for `reasoning.effort`), `usage.include` and
  `provider: {allow_fallbacks: false, require_parameters: true}`. Wrench never retries: one logical request is at
  most one upstream attempt.
- **Settings.** `WRENCH_UPSTREAM_TIMEOUT_{CONNECT,FIRST_TOKEN,STREAM_IDLE,TOTAL}` and
  `WRENCH_UPSTREAM_CONCURRENCY_{PER_USER,GLOBAL}`; concurrency above a limit is `429`.
- **Upstream probe.** A background probe sends `GET key` with Wrench's key every 30 s with a 5 s timeout and caches
  the result; it checks both the network path and the key. (`GET credits` needs a management key, which Wrench does
  not hold.) `/readyz` reads the cache and never calls OpenRouter. Until the first probe finishes the state is
  `unavailable`. A state change publishes a `status` event.
- **Readiness.** `/readyz` means "inference can be served": its checks are `postgres` and `openrouter`. `/healthz`
  is dependency-free.
- **Inference is not gated by the probe.** A request is its own probe; an unreachable upstream fails fast on
  `WRENCH_UPSTREAM_TIMEOUT_CONNECT` and is recorded as `not_sent` ($0).
- **Low balance.** The alert reads `limit_remaining` of Wrench's key from `GET key`. A key without a credit limit
  reports no balance, so the alert stays silent and the runner logs a warning.
- **Reconciliation.** `GET generation?id=<gen-id>` confirms unknown costs, verifies reported ones and fills the
  actual inference provider ([budgets](../budgets.md)).
