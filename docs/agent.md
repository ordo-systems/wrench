# Clients

Wrench ships no client of its own. Developers connect an unmodified [opencode](https://opencode.ai) 2.3.x through its
OpenCode Console integration: one device login, then provider, policies, instructions and budgets come from the
server. Setup: [agent/opencode](../agent/opencode/README.md). Any OpenAI-compatible client with a device key can call
the server directly too.

## What a client sends

- `POST /v1/chat/completions` with `Authorization: Bearer wrk_...` (a device key) and `X-Wrench-Session-Id`
  or opencode's `X-Opencode-Session-Id` (1–128 characters of `[A-Za-z0-9_.:-]`); `X-Wrench-Request-Id` is optional. The model must belong to the
  caller's tier (`GET /api/models`); anything else is refused before an upstream call.
- Refusals keep their reason: `402` for an exhausted budget, `403` for a model outside the tier,
  `429` for the concurrency limit, `503` when accounting is unavailable. Reached budget thresholds come back in
  `X-Wrench-Budget-Warning`.
- `GET /api/budget` and `GET /api/status` tell a client its windows and whether inference is paused.

A session is `active` while requests arrive and `offline` after `WRENCH_SESSION_OFFLINE_AFTER` without one; that
does not prove the client stopped. Conversation history stays on the client.

## Instructions

Wrench manages no agent instructions: `/api/v2/instructions` returns an empty list, so opencode uses only the
project's own AGENTS.md. Wrench never overwrites project AGENTS.md files or stores transcripts.
