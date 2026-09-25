# micro-acp

A small, entirely Go terminal client for [Agent Client Protocol](https://agentclientprotocol.com/). Built on [BrokkAi/acp-go](https://github.com/BrokkAi/acp-go), Bubble Tea v2, Bubbles v2, Lip Gloss v2, and Glamour v2.

Stream conversations, choose models and modes, review tools and diffs, answer permission and input requests, attach files, and manage sessions without leaving your terminal. See the [ACP support matrix](docs/acp-support.md) for protocol coverage and limitations.

## Run

Requires **Go 1.27.1 or newer**, matching `acp-go`'s toolchain requirement. Linux and macOS are the supported targets.

```sh
go build -o bin/micro-acp .
./bin/micro-acp --demo
```

The demo runs a local ACP subprocess with persistent sessions and streaming text. It needs no model credentials and performs no coding work. Try `/mode` or `/settings`, send `permission` for an approval prompt, `form` for structured input, or `slow` to test cancellation. Demo settings reset when its process restarts.

Connect to a real agent:

```sh
./bin/micro-acp                         # Search the registry; Enter connects
./bin/micro-acp agents                 # Fetch and list current registry entries
./bin/micro-acp --agent <registry-id>
./bin/micro-acp --cwd /path/to/project --agent <registry-id>
```

Agents use their own accounts and authentication. Existing environment variables and credentials are inherited. `/auth` opens the offered login methods; `/auth <method-id>` selects one directly. Agent-managed login displays stderr output and handles input requests. Terminal login temporarily suspends the TUI, runs the agent's advertised login flow, then reconnects and reinitializes the agent. Authentication-required errors open the login picker automatically.

## Custom agents

Pass a stdio ACP executable and its arguments after `--`:

```sh
./bin/micro-acp -- /absolute/path/to/my-agent --acp
./bin/micro-acp --cwd /path/to/project -- my-agent serve
```

Commands are executed directly as argv, without a shell. Flags for micro-acp must precede `--` or the subcommand. The executable must speak ACP over stdin/stdout and send its logs to stderr.

For reusable configurations, run `micro-acp config` to see paths and an example, then create the indicated `config.json`:

```json
{
  "default_agent": "my-agent",
  "agents": {
    "my-agent": {
      "command": "/absolute/path/to/my-agent",
      "args": ["--acp"],
      "env": { "MY_AGENT_SETTING": "value" }
    }
  }
}
```

Custom agent names take precedence over matching registry IDs. Keep secrets in your environment when possible. A different file can be selected with `--config /path/to/config.json`.

Configure MCP servers and additional workspace directories in the same file:

```json
{
  "session": {
    "additional_directories": ["/absolute/path/to/shared-library"],
    "mcp_servers": [
      {
        "name": "local-tools",
        "command": "/absolute/path/to/mcp-server",
        "args": [],
        "env": []
      },
      {
        "type": "http",
        "name": "remote-tools",
        "url": "https://example.com/mcp",
        "headers": []
      }
    ]
  }
}
```

MCP entries use ACP's stdio, HTTP, or SSE server schema. The agent manages these servers. Optional transports and additional directories require advertised agent support. Relative additional directories resolve against `--cwd`; saved sessions retain their directory list. Current MCP configuration is sent on new, load, resume, and fork operations.

## Sessions

| Command | Behavior |
| --- | --- |
| `/new` | Create a new agent session. |
| `/close` | Close the active session on the agent while preserving saved history; requires agent support. |
| `/sessions` | Search saved sessions and the agent's session list for the current workspace. |
| `/load <local-id>` | Resume a saved session using the agent's resume or load capability. |
| `/fork` | Fork natively when the agent advertises the optional `session/fork` capability. |
| `/fork --context` | Create a new session and attach saved user/assistant text to its first prompt. |
| `/delete` | Confirm deletion from both the agent and local history; requires agent support. |
| `/forget` | Confirm removal from local history only. The agent keeps its copy. |

Session metadata and transcripts survive restarts. List and resume them from the shell:

```sh
./bin/micro-acp sessions
./bin/micro-acp --session <local-id>
./bin/micro-acp sessions forget <local-id>
```

Resuming infers the saved agent and workspace. Named custom agents must still exist in your configuration. For an inline custom command, supply that same command again after `--`. The session picker only lists the connected agent's current workspace; use the shell's `sessions` command to see all workspaces.

Native session operations depend on the agent's advertised capabilities. A context fork preserves text conversation only: it does not clone the agent's hidden state, tool history, or workspace files. No historical requests are replayed as separate prompts. Loading an existing session requires agent support. `/forget` does not delete remote sessions, so they may reappear in the agent's session list.

## Models, tools, and context

`/model`, `/mode`, and `/effort` open searchable selectors using the agent's current options. `/settings` exposes every offered select or boolean option, including options with custom categories. Values can also be passed directly, such as `/model <id>`. Model changes refresh dependent choices such as reasoning effort. The header shows selections, context usage, and cost when the agent reports them.

The transcript includes Markdown responses, reasoning, plans, tool input/output, file locations, full old/new diffs, and live client terminal output. Tool updates retain earlier details when a later update only changes status. Non-text content is preserved in session files; images and audio appear as descriptive markers in the terminal.

Agent-provided slash commands appear in the command palette. Unknown slash commands are sent to the agent as prompts. If an agent command shares a client command's name, use `/agent <command>`, for example `/agent new`. Compaction is available through an agent's advertised command when it offers one.

Attach context using:

```text
Review @internal/client/client.go
Explain @"docs/a file with spaces.md"
/attach /path/to/screenshot.png
/resource https://example.com/specification
/detach
```

Tab after `@` opens a workspace file picker. `/attach` queues a file for the next prompt; `/detach` clears queued attachments and resource links. Each attachment is limited to 4 MiB. Text uses embedded context when supported and plain text otherwise. Images, audio, and binary resources require the matching agent capability. Resource links are passed to the agent without fetching them. Enter can send queued attachments without accompanying text.

Form requests support strings, numbers, integers, booleans, single choices, and multiple choices, with defaults and validation. Enter advances, Shift+Tab goes back, Space toggles multiple choices, and Ctrl+X skips an optional field. Review answers before selecting Submit. Ctrl+D declines and Esc cancels. URL requests show the agent, host, and complete address; selecting Submit opens the system browser. No URL opens automatically.

## Keyboard and commands

| Key | Action |
| --- | --- |
| Enter | Send prompt or select a list entry. |
| Alt+Enter / Ctrl+J | Insert a newline. Shift+Enter also works in compatible terminals. |
| Ctrl+P | Command palette. |
| Ctrl+G | Agent picker. |
| Ctrl+S | Session picker. |
| Ctrl+N | New session. |
| Tab | Complete a slash command or open the `@file` picker. |
| Up / Alt+Up, Down / Alt+Down | Recall prompt history; Up works on the first input line. |
| Page Up / Page Down / mouse wheel | Scroll the conversation. |
| Esc | Cancel the active turn or close a picker. |
| Ctrl+C | Cancel an active turn; otherwise quit. |
| `/` in a picker | Filter its entries. Enter applies the filter; Enter again selects. |
| Ctrl+D in the session picker | Confirm deletion, or local removal if remote deletion is unsupported. |

Other commands: `/agents`, `/help`, `/info`, `/auth`, `/logout`, `/refresh`, `/quit`. `/info` displays advertised capabilities and configuration. `/logs` shows recent agent stderr and the last error; `/reconnect` restarts the agent and reloads the saved session when supported. `/logout` requires the agent's logout capability.

Permission dialogs show the tool details and the agent's choices and default to **Cancel**. Page Up/Down scroll long permission and input requests. No automatic approval is enabled. Filesystem and terminal callbacks use `acp-go/clienthost`, rooted at the workspace and configured additional directories. This client keeps one active session per agent connection and releases its terminals on session switches. Agents and their subprocesses run with your account's OS permissions; this is not an OS sandbox.

## Registry and storage

At startup, micro-acp revalidates the [official latest registry](https://github.com/agentclientprotocol/registry) at `https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`. `/refresh` checks it again. There is no bundled list of agent versions. The next agent connection uses the selected registry distribution; already running processes are not replaced in the middle of a session.

Supported distributions:

- `npx`: requires Node.js/npm; launches the registry's exact package spec with `npx --yes`.
- `uvx`: requires `uv`; launches the registry's package spec.
- Native binaries: downloads for the current OS/architecture, verifies SHA-256 when supplied, and installs into a private cache. ZIP, tar.gz, tar.bz2, and raw executable distributions are supported. Archives containing symlinks, special files, or unsafe paths are rejected.

When a refresh fails, a valid cached registry is used with a warning. With no cache, custom agents remain available in the TUI. `--offline` deliberately uses the cache; it does not guarantee that an agent's package manager or the agent itself can run without network access. The demo does not fetch the registry.

Default paths follow the XDG variables:

| Data | Default path |
| --- | --- |
| Configuration | `~/.config/micro-acp/config.json` |
| Sessions | `~/.local/state/micro-acp/sessions/` |
| Registry and binaries | `~/.cache/micro-acp/` |

Set `MICRO_ACP_HOME` to keep config, data, and cache under one directory. Session files are written atomically with private permissions. Saving happens before and after each prompt and on orderly shutdown; a hard crash can lose the currently streaming response. Concurrent instances should use different sessions, since writes to the same saved session use last-writer-wins semantics.

The client targets stable **ACP v1** using `BrokkAi/acp-go v0.10.0`. Native forks additionally use the SDK's opt-in unstable v1 schema. Draft ACP v2 and editor-specific experimental extensions are not advertised. The [support matrix](docs/acp-support.md) maps protocol methods to UI flows and tests.

## Development

```sh
make build
make test       # Race detector, including real ACP subprocess integration tests
make vet
```

Tests require no external agents, model accounts, or internet access. Registry tests use loopback HTTP/TLS servers. CI runs on Linux and macOS.

The implementation is split into `internal/client` (ACP and process lifecycle), `internal/registry` (discovery/install), `internal/store` (sessions), `internal/config`, `internal/forms`, `internal/tui`, and `internal/demo`.
