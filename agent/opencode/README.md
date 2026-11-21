# opencode with Wrench

Developers use an unmodified [opencode](https://opencode.ai).
It signs in to Wrench through its OpenCode Console integration and receives everything else from the server:
the `wrench` provider with your tier's models, policies that allow no other provider,
the managed `AGENTS.md`, and your budgets for `/usage`.

## Supported version

opencode **2.3.x**. Wrench answers opencode's Console API, which is private and undocumented,
so a newer opencode can break sign-in or setup.
Upgrade only after `make opencode-contract` passes with the new version.

## Install

- macOS: `brew install opencode`.
- Linux or Windows: the `v2.3.0` archive for your platform from the
  [opencode releases](https://github.com/anomalyco/opencode/releases/tag/v2.3.0).

npm's `opencode-ai` package is the 1.x line and does not work with Wrench.

## Sign in

```sh
opencode auth login opencode --method device --answer server=https://wrench.example.com
```

Opencode prints a code and opens `https://wrench.example.com/device?code=...`.
Sign in with GitLab if asked, check that the code matches, and press **Approve**.
Opencode then shows "Connected to OpenCode Console", and `/models` lists only Wrench models.

Sign out with `opencode auth logout`.
An administrator can revoke your device key on the dashboard; opencode then asks you to reconnect.

## What you get

- **Models:** your tier's models from the `wrench` provider.
  Prompts to other providers are blocked by policy, even when their keys are in your environment.
- **`/usage`:** your 5-hour, day, week and month budgets (administrators also see the shared ones).
- **Instructions:** Wrench adds none; opencode uses your project's `AGENTS.md`.
- **Subagents** run on the same tier models and appear as separate sessions on the dashboard.
- **Sessions** on the dashboard use opencode's session ids and show the signed-in device.

Turn off opencode's `/share`: it uploads conversations to [opencode.ai](https://opencode.ai).

## Recommended skills and plugins

Install these tools outside the repository.
The steps below are for opencode 2.x; global configuration lives in `~/.config/opencode/opencode.json`.
Restart opencode after each change.

### RTK

[RTK](https://github.com/rtk-ai/rtk) shortens command output before it reaches the agent's context.

```sh
brew install rtk          # macOS, Linux with Homebrew
# or: curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh
# Windows: winget install rtk-ai.rtk
rtk init -g --opencode    # writes ~/.config/opencode/plugins/rtk.ts
rtk gain                  # verify: shows the savings dashboard
```

`rtk init -g --opencode` also installs the Claude Code hook.
crates.io has an unrelated package that is also called `rtk`; if `rtk gain` is not recognized, the wrong binary is installed.
See the [supported agents](https://www.rtk-ai.app/docs/getting-started/supported-agents/).

### Superpowers

[Superpowers](https://github.com/obra/superpowers) adds skills for brainstorming, planning, TDD, systematic debugging and verification.
Add it to `opencode.json`; opencode's plugin manager installs it and registers its skills:

```json
{
  "plugins": ["superpowers@git+https://github.com/obra/superpowers.git"]
}
```

Pin a release with `#v7.0.2` at the end of the URL.
Verify by asking "Tell me about your superpowers".
See the [opencode install guide](https://github.com/obra/superpowers/blob/main/.opencode/INSTALL.md).

### ub-mode

[ub-mode](https://github.com/unclebob/ub-mode) is a single skill for writing, refactoring and reviewing code.
Copy its `SKILL.md` into a skills directory named `ub-mode`:

```sh
mkdir -p ~/.config/opencode/skills/ub-mode
curl -fsSL https://raw.githubusercontent.com/unclebob/ub-mode/HEAD/SKILL.md \
  -o ~/.config/opencode/skills/ub-mode/SKILL.md
```

Opencode also reads `~/.claude/skills`, so a copy installed for Claude Code works too.
Mention `@ub-mode` in a prompt to load it.
See [opencode skills](https://opencode.ai/docs/skills/).

### Serena

[Serena](https://github.com/oraios/serena) provides symbol lookup, reference analysis and project memories over MCP.
It needs [uv](https://docs.astral.sh/uv/).
Do not install it from an MCP or plugin marketplace: those entries are outdated.

```sh
uv tool install -p 3.13 serena-agent
serena init
```

Then add it as a local MCP server in `opencode.json`:

```json
{
  "mcp": {
    "servers": {
      "serena": {
        "type": "local",
        "command": ["serena", "start-mcp-server", "--context", "ide", "--project-from-cwd"]
      }
    }
  }
}
```

Check it with `opencode mcp list`.
If opencode cannot find `serena`, use the full path from `which serena`.
Ask the agent to activate the current directory as a Serena project before starting work.
See the [Serena client guide](https://oraios.github.io/serena/02-usage/030_clients.html) and [opencode MCP servers](https://opencode.ai/docs/mcp-servers/).

## Contract test

`make opencode-contract` runs the installed opencode against the demo stack: device login approved through the GitLab mock,
`opencode run`, and checks that the request was accounted under opencode's session.
CI runs it with the pinned 2.3.0 release.
