# Wrench for vanilla opencode

Wrench ships no client. Developers run an unmodified [opencode](https://opencode.ai) and connect it to Wrench with
one command; everything the client needs (provider, models, policies, budgets for `/usage`) comes from the server.
The implementation is the [console facade](06-opencode-console.md).

## Why

opencode's "OpenCode Console" integration accepts a custom server: its device login takes a hidden `server` form
field, and every later Console call goes to that server. A Wrench that answers the Console API gives a vanilla
opencode a managed setup without shipping or updating client code.

Verified against opencode 2.3.x:

- `opencode auth login opencode --method device --answer server=<url>` runs the device flow against `<url>`.
- A provider delivered by `GET /api/v2/config` calls `<baseURL>/chat/completions` with
  `Authorization: Bearer <console access token>`; the access token is a Wrench `wrk_` key.
- `GET /api/v2/instructions` content reaches the system prompt.
- The built-in `/usage` renders `GET /api/policies/current-budgets`.
- `provider.use` policies hide every other provider, even with `OPENROUTER_API_KEY` in the environment.
- Requests carry `x-opencode-session-id: ses_...`; a 402 `{error: {message}}` is shown as `Error: <message>`.

## Console contract (opencode 2.3.x)

All bodies are JSON. Console calls carry `Authorization: Bearer <access token>` and `x-org-id`.

| Call                                | Request                                                                                                                                             | Response                                                                                                                                                       |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `POST /auth/device/code`            | `{client_id: "opencode-cli", supports_org_scope: true}`                                                                                             | `{device_code, user_code, verification_uri_complete, expires_in, interval}`                                                                                    |
| `POST /auth/device/token`           | `{grant_type: "urn:ietf:params:oauth:grant-type:device_code", device_code, client_id}` or `{grant_type: "refresh_token", refresh_token, client_id}` | `{access_token, refresh_token, expires_in, org_id?}`; 400 `{error: authorization_pending \| slow_down \| invalid_grant \| ...}`                                |
| `GET /api/user`                     |                                                                                                                                                     | `{id, email}`                                                                                                                                                  |
| `GET /api/orgs`                     |                                                                                                                                                     | `[{id, name}]`                                                                                                                                                 |
| `GET /api/v2/config`                |                                                                                                                                                     | `{providers, mcp, policies, websearch}`; header `x-opencode-instructions-skills` enables the next two; 401 `{_tag: "Unauthorized"}` asks the user to reconnect |
| `GET /api/v2/instructions`          |                                                                                                                                                     | `{instructions: [{id, content}]}`                                                                                                                              |
| `GET /api/v2/skills`                |                                                                                                                                                     | `{skills: [{slug, version}]}`                                                                                                                                  |
| `GET /api/policies/current-budgets` |                                                                                                                                                     | `[{ruleID, limitDollars, spentDollars, scope, per, coverage, models, vendors, inputPriceAtLeast?, picked, shared, resetsAt}]`; 403 means none                  |
| `GET /zen/go/v1/usage`              |                                                                                                                                                     | 403 hides the OpenCode Go block                                                                                                                                |

The contract is private and undocumented. `make opencode-contract` runs the real opencode binary against the demo
stack to guard it; the supported version is stated in [agent/opencode](../../agent/opencode/README.md).

## Out of scope

- Per-role subagent control. opencode subagents are child sessions in the same process; they reach Wrench as
  separate sessions with the parent tier's models.
- Client-side enforcement beyond Console policies (system or MDM opencode configuration).
- Managed instructions and skills (`/api/v2/instructions` and `/api/v2/skills` answer empty lists), MCP and web
  search through Wrench.
