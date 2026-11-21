# Coding-agent platform: product requirements

Wrench gives a team of about 20 people a coding agent with centrally managed access and spending. The priority is
the lowest cost per seat at acceptable task quality. Parity with any vendor subscription is not promised.

## Composition

```text
opencode (unmodified, on the developer's machine)
                  |
                  v
Wrench (Go): access, budgets, models, accounting, metrics, dashboard
                  |
                  v
             OpenRouter
```

- The agent, its tools, tests and repository work run locally on the developer's machine. Every model call goes
  through Wrench; clients never hold provider keys or GitLab tokens.
- Wrench is bound to OpenRouter on purpose: budgets need its reported prices ([upstream](07-openrouter-direct.md)).
- Wrench owns authentication and authorization, device keys, user and global budgets, the model allowlist per tier,
  the attempt journal, metrics, alerts, session presence and the dashboard. Admission and accounting decisions are
  made only by Wrench.

## Platform and configuration

- One Go monolith with PostgreSQL for users, keys, sessions, the attempt journal, accounting and background state.
  The Vue dashboard is embedded in the binary.
- `config.yaml` holds models, budgets, tiers, the email allowlist with explicit administrators, and alerts
  ([format](01-server-config.md)); it reloads without a restart and an invalid edit never removes limits
  ([reload](01-server-reload.md)). Runtime settings and secrets come from the environment
  ([variables](01-server.md#environment-variables)).
- The dashboard never writes configuration; operational actions (revocation) do not edit YAML.

## Client

- Developers use an unmodified opencode 2.3.x through its OpenCode Console integration: one device login, then the
  provider, policies and budgets come from Wrench ([opencode](06-opencode.md)).
- Conversation history stays on the client; Wrench stores no transcripts and manages no agent instructions.
- opencode subagents are child sessions that reach Wrench as separate sessions with the parent tier's models.

## Access and keys

- Sign-in is GitLab OIDC with PKCE, a verified email on the allowlist, and an identity bound once to
  `(issuer, sub)` ([authentication](../authentication.md)).
- Device keys are issued only through the device login, expire after `WRENCH_CLIENT_KEYS_TTL`, and are revoked when
  GitLab confirms an account is blocked, locked or removed. Every device of a user shares one budget.

## Budgets

- Per-user limits from the tier and installation-wide limits, in fixed 5-hour, day and week windows from a common
  UTC anchor and the calendar month ([budgets](../budgets.md), [month](01-server-month.md)).
- Admission checks confirmed spend before forwarding and reserves nothing; admitted requests may overrun, and the full
  charge is recorded. Unused allowance never carries over.
- Unknown costs never pause inference; they are reconciled from OpenRouter and marked on the affected windows
  ([unsettled costs](12-unsettled-without-pause.md)).
- Accounting storage unavailable means no new inference.

## Models and execution

- Each tier lists the models its users may select, with one default. A model outside the tier is refused before an
  upstream call; a failed or exhausted model is never substituted automatically.
- One logical request is at most one upstream attempt; Wrench never retries and disables provider fallback.
- Token limits and prices come from the OpenRouter catalog.

## Journal and metrics

- Every attempt records user, session, device, model, upstream provider, generation id, timings (start, first token,
  end), outcome, tokens (input, output, reasoning, cache read and write as reported) and cost with its reconciliation
  state. Missing values stay null, never zero.
- Prompts, completions, source code and secret values are never logged.
- Metrics: confirmed spend, tokens, requests by outcome, time-to-first-token percentiles, cached share of input and
  active sessions, by period, model and user; budgets with spend, limit and reset. Charts mark incomplete telemetry.

## Dashboard and alerts

- Users see their own usage, budgets, sessions and keys; administrators also see the system, users and alerts
  ([dashboard](08-dashboard-redesign.md), [roles](05-dashboard-roles.md)).
- Alerts cover budget thresholds and exhaustion, OpenRouter unreachable, low key balance, accounting storage,
  unavailable models, error rate, first-token latency, invalid configuration and backups. A continuing incident is
  one notification, repeated at most every `repeat_after`. A stopped node cannot alert: external monitoring covers
  node failure.

## Deployment and backup

- One node with Docker Compose and four containers: `caddy` (HTTPS), `wrench`, `postgres` and `backup`
  ([deployment](01-server-deploy.md)).
- The backup container runs its own UTC schedule (no host cron), dumps PostgreSQL consistently and stores the dump
  with the configuration in an encrypted local repository and in S3. Target RPO is one backup interval, target RTO
  four hours; restores are drilled, not assumed. No high availability.

## Acceptance scenarios

1. An allowlisted user signs in through GitLab; anyone else is refused, even after a successful GitLab login.
2. An expired or revoked key cannot make model requests.
3. Blocking a user in GitLab revokes all their keys and sessions after the next status check.
4. Several devices of one user spend one user budget.
5. Exhausting any applicable user or global window refuses new requests with `402`.
6. Admitted requests may overrun a budget; costs are recorded in full and never twice.
7. With accounting storage unavailable, no request reaches OpenRouter.
8. A new window starts with the full limit; nothing carries over.
9. A request is charged to the windows of its start time, including late reconciliation.
10. A lost final usage chunk never makes a request free; reconciliation picks it up.
11. A model outside the tier is refused; an unavailable model is never replaced by another.
12. A session without requests becomes `offline` after `WRENCH_SESSION_OFFLINE_AFTER`.
13. Charts match the journal, including late costs and missing usage fields.
14. A user never sees another user's data; an administrator sees the whole system.
15. Restarting Compose keeps the accounting; restoring from S3 returns a working system.
16. Provider keys, prompts and code never appear in server logs.

## Rollout checks

- Verify the company GitLab's email claims and account-state permissions.
- Run the live check against the real OpenRouter for every tier model: streaming, tool calls, cancellation and cost
  reconciliation.
- Drill a restore from the real S3 bucket.
- Tune budgets and tier models from pilot measurements.
