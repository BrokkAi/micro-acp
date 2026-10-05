# ACP support

Target: stable ACP v1 as represented by `github.com/BrokkAi/acp-go` v0.12.1, plus the opt-in [ACP v2 draft](#acp-v2-draft). All 25 methods in that stable schema have a client path or are handled by the SDK transport. Optional operations depend on negotiated agent capabilities. This is implementation and fixture coverage, not certification against every registry agent.

## Methods

| ACP method | Client behavior | Verification |
| --- | --- | --- |
| `initialize` | Version negotiation, identity, filesystem/terminal, boolean settings, form/URL input, and terminal auth capabilities. | Subprocess fixtures; rejects incompatible versions. |
| `authenticate` | `/auth` method picker, agent login output and input requests. | `TestElicitationAndAuthenticationRoundTrips`. |
| `logout` | `/logout`; capability checked by SDK, saves history and clears active session. | `TestElicitationAndAuthenticationRoundTrips`. |
| `session/new` | `/new`, optional MCP servers and additional directories; handles setup callbacks before response. | Lifecycle, resume, workspace tests. |
| `session/load` | `/sessions` or `/load`; reconstructs transcript from replay, retaining saved history when no replay arrives. | `TestSessionLifecycleAcrossProcesses`. |
| `session/resume` | Preferred for saved history; remote-only sessions use load replay when available. Refreshes session configuration. | `TestResumeKeepsLocalTranscriptAndRefreshesState`, `TestRemoteOnlySessionLoadsHistoryWhenResumeIsAlsoAvailable`. |
| `session/list` | Combines saved sessions with paginated remote results, filters by workspace, deduplicates. | Lifecycle tests. |
| `session/close` | `/close`; saves local history, closes remote session, releases terminals. | Authentication/lifecycle fixture. |
| `session/delete` | `/delete` or session picker; explicit confirmation, remote deletion followed by local removal. `/forget` is local only. | Lifecycle tests. |
| `session/prompt` | Multiline composer, streamed response, rich content, agent slash commands. Token/request limits and refusals show their stop reason and pause queued prompts. | Lifecycle, configuration, rich content and stop-reason tests. |
| `session/cancel` | Esc/Ctrl+C stops a turn, marks its unfinished tools cancelled locally, cancels pending interactions, waits for completion; disconnects unresponsive processes. Final agent tool results take precedence over local cancellation. | Permission/cancellation tests. |
| `session/set_mode` | `/mode` for agents offering legacy session modes. | `TestAgentDrivenConfigurationAndRichUpdates`. |
| `session/set_config_option` | `/config` (`/settings` alias), option/value completion, plus `/model`, `/mode`, `/effort`; grouped choices, booleans, arbitrary categories, and dependent option refresh. | `TestAgentDrivenConfigurationAndRichUpdates`. |
| `session/update` | All 11 stable update variants are projected; details below. | Rich updates and replay tests. |
| `session/request_permission` | Scrollable tool details and the exact agent-provided options, Cancel by default. | Permission integration tests and TUI tests. |
| `fs/read_text_file` | Workspace-rooted UTF-8 reads, optional line range, via `clienthost`. | `TestMCPRootsFilesystemAndAllTerminalCallbacks`. |
| `fs/write_text_file` | Workspace-rooted writes, including configured additional directories. | Same workspace test. |
| `terminal/create` | Starts argv in an allowed workspace, maps terminal IDs across roots, streams output into transcript. | Same workspace test. |
| `terminal/output` | Returns bounded output, truncation state and exit status. | Same workspace test. |
| `terminal/wait_for_exit` | Waits with request cancellation support. | Same workspace test. |
| `terminal/kill` | Terminates the client terminal process via `clienthost`. | Same workspace test. |
| `terminal/release` | Saves final visible output and releases terminal resources. | Same workspace test. |
| `elicitation/create` | Form and URL flows, session/request scopes, validation, review/edit, accept/decline/cancel. Also available during initialization and authentication. | Elicitation integration, forms and TUI tests. |
| `elicitation/complete` | Reports completion for a known URL interaction; ignores unknown or duplicate completion IDs. | `TestElicitationAndAuthenticationRoundTrips`. |
| `$/cancel_request` | SDK transport cancels pending callbacks; canceled forms and permission dialogs leave the UI queue. | `TestAgentCancelsElicitationAndNextTurnWorks`. |

Terminal authentication is a separate process flow rather than an `authenticate` call. The client preserves its configured executable, appends the advertised arguments/environment, suspends the TUI, checks the exit status, and reconnects/reinitializes on success. A saved active session is loaded again when the agent supports it. See the [authentication specification](https://agentclientprotocol.com/protocol/v1/authentication).

## Session updates and content

| Update | Presentation and state |
| --- | --- |
| `user_message_chunk` | Restores replayed prompts during load and native fork. |
| `agent_message_chunk` | Streams Markdown, retains message IDs and non-text blocks. |
| `agent_thought_chunk` | Compact thinking entries; full text in Ctrl+O details. |
| `tool_call` | Compact title/status and file-change summary; Ctrl+O exposes kind, name, inputs, content, file locations and outputs. |
| `tool_call_update` | Merges partial updates without discarding omitted fields; null raw input/output preserves prior values, while explicit empty lists replace them. |
| `plan` | Live checklist above the prompt with status markers, priorities and completion counts. Long plans follow the active step; Ctrl+O retains full update history. |
| `available_commands_update` | Refreshes inline slash suggestions; resolves client command collisions through `/agent`. |
| `current_mode_update` | Updates the current legacy mode. |
| `config_option_update` | Refreshes open selectors, completions, and the persistent status line, including custom options, On/Off values, and model-dependent choices. |
| `session_info_update` | Updates title and timestamp; Codex thread-status metadata also tracks detached steering continuations. |
| `usage_update` | Stores used/capacity and reported cost; the status line displays the percentage of context remaining. |

Prompts can carry text, resource links, images, audio, embedded text resources and embedded binary resources. Capability checks happen before sending. Full content blocks persist in local session files. The TUI renders text/resources and labels images, audio and binary data; it does not render bitmap images or play audio. Tool content supports text/resources, diffs and terminal references. `TestRichContentRoundTripAndPersistence` verifies these survive the actual stdio transport and disk storage.

Form fields support the restricted flat schema: strings, integers, numbers, booleans, string enums, titled choices and multiple choices, with defaults and constraints. A raw overlay reads `requestedSchema`, `url` and `elicitationId`, which acp-go releases before v0.12.1 dropped, and the older `schema` spelling. Transport, schema validation types and response unions still come from `acp-go`. See [ACP elicitation](https://agentclientprotocol.com/protocol/v1/elicitation).

## Extensions and boundaries

- `_session/steering`: enabled only by top-level `initialize._meta.steering.supported`. Enter injects guidance; Tab retains a local FIFO follow-up. The client requests `idleBehavior: promptRequired`, so Claude can return unconsumed late input for one normal `session/prompt`. Codex's `startedNewTurn` is already consumed: its `_meta.codex.threadStatus` updates keep the turn busy until completion. Accepted steering is recorded once; rejected or ambiguous deliveries remain in a paused queue. See the [Claude example](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/examples/steering.ts) and [Codex extension](https://github.com/agentclientprotocol/codex-acp/blob/main/src/AcpExtensions.ts). In-memory ACP transport tests cover acceptance, non-delivery, unknown outcomes, and detached completion before/after acknowledgment; TUI tests cover queue order, editing, deletion, draft/context retention, and fallback.
- `session/fork`: implemented through the SDK's unstable v1 schema only when advertised. Replayed history replaces the inherited transcript; agents that do not replay retain the saved history. Tested by `TestNativeForkUsesAdvertisedCapability` and `TestNativeForkReplayReplacesInheritedHistory`. `/fork --context` is a separately labeled text-context fallback.
- Subagent sessions ([RFD #1992](https://github.com/agentclientprotocol/agent-client-protocol/pull/1992), unstable): `initialize` sends `clientCapabilities.subagents: {}`. Three wire shapes are accepted and mapped to one child model (ID, parent, name, task, prompt, state):
  - the first draft, used by claude-agent-acp and codex-acp: `subagent_spawned` (`subagentSessionId`, `name`, `task`, optional `prompt`) and one `subagent_state_update` with `completed`, `failed`, `cancelled` or `disconnected`;
  - codegraff's `GRAFF_ACP_DRAFT_SUBAGENTS=1` variant: `subagent_update` with those same old fields;
  - the reworked draft in acp-go `schema/unstable`: `subagent_update` (`sessionId`, `title`, `description`, `state` of `running`, `idle` with `stopReason`, `requires_action` or `unknown`, `capabilities.cancel`), plus `session_message` and `session_message_chunk`.

  Each announcement arrives on the child's immediate parent, so children can nest. After it, the child's messages, thoughts, plans and tool calls go to the child's own transcript, and its `session/request_permission` and `elicitation/create` requests are accepted and show which subagent is asking. File and terminal callbacks from a child run in the root session's workspace. Updates and requests for session IDs that were never announced are still dropped or rejected. A resumed child (`<id>:generation:<n>`) is a new child row labeled with its run number.

  The parent transcript shows one row per child with its name and state, and reprints the row when the state changes. `/subagents` lists the tree; Enter opens a child's transcript. Ctrl+X there sends `session/cancel` to that child only when it advertises `capabilities.cancel`; otherwise Esc stops the whole turn. Children are saved with the session. On `session/load` the replayed tree replaces the saved one; children kept from local history that were still active are shown as `unknown`. `exec` applies its permission policy to child requests; `--format json` adds a `subagents` list with each child's text, and `stream-json` adds `subagent` events and tags child output with `"subagent": <id>`. The demo agent's `subagent` prompt shows a nested child, a child permission request and a per-child stop.

  Checked live on 2026-10-05: claude-agent-acp 0.85.1 sends native children, and its child tool calls land in the child. codex-acp 2.1.1 advertises `sessionCapabilities.subagents`, but its bundled ACP SDK parses `clientCapabilities` with a schema that has no `subagents` field and drops it, so it falls back to plain "Start subagent" tool calls. Its only other opt-in is a JetBrains AIR `_meta` key, which also switches it to AIR's tool-call format, so this client does not send it. Native Codex children need a codex-acp release with a newer bundled SDK.
- MCP configuration: stdio, HTTP and SSE definitions are forwarded during session operations. The agent connects to and operates those servers. This client is not an MCP server or an MCP-over-ACP proxy.
- Additional directories: passed only through supported session operations, restored with saved sessions, and included in client callback routing.
- Unknown notifications are ignored; unsupported requests receive JSON-RPC method-not-found. No editor document synchronization, inline completion, next-edit prediction or experimental provider-management capabilities are advertised.
- Arbitrary third-party extensions, inline image display and audio playback are outside this implementation. The ACP v2 draft is opt-in; see below.
- Only one active conversation per connection is presented. Session switches release client terminal processes. Sessions and custom agents can be switched from the UI.
- Registry freshness concerns discovery and the next agent launch. An already-running process is not upgraded during a turn.

Run `make test` for the race-enabled suite and `make vet` for static checks. Tests use local subprocess agents and loopback registry servers; they do not use live model accounts. `--demo` exercises streaming, session history, settings, permission requests, forms and cancellation without credentials.

## ACP v2 draft

Opt-in with `--acp-v2` or `"protocol": "v2"` on a configured agent; v1 stays the default. The pinned draft is `schema-v2.0.0-alpha.7` from acp-go v0.12.1.

- **Negotiation.** acp-go's `clientrouter` sends a v2 `initialize`. An agent that answers v1 continues on v1 under the router's rules (it reconnects with a fresh process when the v1 parameters differ). A v2 rejection is reported and never retried as v1.
- **Capabilities.** A v2 connection advertises only terminal auth and form/URL input. v2 removed `fs/*` and `terminal/*`, so they are neither advertised nor served.
- **Prompts and turns.** `session/prompt` returns as soon as the agent takes the message in (`messageId`); the turn ends at the next `state_update` `idle`, tracked with acp-go's `SessionTracker`. The prompt's `user_message` echo is matched to the local prompt by ID, before or after the acknowledgement. `requires_action` shows as "waiting on you". An `idle` with stop reason `_error` is a failed turn; its JSON-RPC error is read from `_meta` (`claudeCode.error`, `graff/error`). A `running` with no prompt from this client is a turn the agent started (codegraff's peer mail) and shows as work in progress until `idle`.
- **Cancel.** Esc cancels pending permission requests through acp-go's `CancellablePermissions` and sends `session/cancel`; the turn ends at the agent's `idle` with `cancelled`. It also stops an agent-started turn.
- **Messages.** `user_message`, `agent_message` and `agent_thought` upserts and their `*_chunk` appends are keyed by role and `messageId`, so a replayed message that is first cleared with empty content and then streamed again is rebuilt in place.
- **Tools.** `tool_call_update` upserts with v2 patch semantics (omitted keeps, null clears) and `tool_call_content_chunk` appends. Structured file changes (`add`, `delete`, `modify`, `move`, `copy`) and their patch are kept with the tool call and shown as file rows and a colored patch in Ctrl+O. A permission request's `subject.toolCall` also updates the tool call, since Claude sends an edit's changes only there.
- **Agent terminals.** `terminal_update` and `terminal_output_chunk` (base64) build a display-only terminal entry with command, output and exit status.
- **Other updates.** `plan_update` checklists drive the live plan (Markdown plans show as a message, plan files as a notice) and `plan_removed` clears it. `notice`, `compaction_update` and `compaction_summary_chunk` show as notices. `usage_update`, `session_info_update`, `available_commands_update` and `config_option_update` work as in v1; modes are config options. Options that use the v1 key `id` instead of `configId` (codegraff 0.0.302) are accepted.
- **Sessions.** `session/new` (with MCP servers through `v2/mcp`), `session/list` (an agent without it lists nothing), `session/resume` without replay when a local transcript exists, or `ResumeSessionFromStart` to rebuild one, `session/fork` when advertised, `session/close`, `session/delete`, `session/set_config_option`, `auth/login` and `auth/logout`. Steering is not used on v2.
- **Subagents.** No agent sends them on v2 yet. When one does, `subagent_update` announces the child as in v1 and the child's `state_update` on its own session sets its state in the same child view.

Tests run against fake agents built on acptest's draft-v2 `V2Agent` and acp-go's v2 agent runtime: prompt acknowledgement, idle ending the turn, `requires_action`, `_error` as a failure, cancel, resume from start, agent-started turns, file changes, agent terminals, child state, fallback to v1, and no v1 retry after a v2 rejection.

Checked live on 2026-10-05:

- codegraff 0.0.302.6 with `GRAFF_ACP_V2=1`: prompt acknowledgement, `user_message`, `running`, tool updates, `usage_update`, and `idle` ending the turn.
- claude-agent-acp `main` (5598efb) with `CLAUDE_AGENT_ACP_EXPERIMENTAL_V2=1`: an agent terminal, `requires_action` around a Write permission (its diff kept with the tool call), `idle` ending the turn, and a resume from start that rebuilt the transcript. The released 0.85.1 has only the v2 handshake.

## Coverage review: 2026-09-26

The latest stable upstream release checked was [schema-v1.23.0](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.23.0). Its downloaded `meta.json` exactly matches the pinned SDK's stable method registry: 13 agent methods, 11 client methods and one protocol notification. The implementation paths above cover all 25, and all 11 stable [session update variants](https://agentclientprotocol.com/protocol/v1/prompt-turn#session-updates) have handlers. This review identified no missing stable method family. Negotiated capabilities still determine which optional methods a connected agent supports.

Behavioral checks include complete [plan replacement](https://agentclientprotocol.com/protocol/v1/agent-plan#updating-plans), explicit [stop reasons and cancellation](https://agentclientprotocol.com/protocol/v1/prompt-turn), and preservation of previous tool input/output when a [partial update supplies null](https://agentclientprotocol.com/protocol/v1/tool-calls). The support matrix describes their presentation and regression coverage. Method coverage and fixture tests do not establish interoperability with every agent or exhaustive conformance for every payload.

The following features remain outside the implementation. Upstream status is recorded as of this review; preview and draft contracts can change.

| Remaining feature | Status and current behavior |
| --- | --- |
| Compaction lifecycle and summaries | [Preview](https://agentclientprotocol.com/rfds/session-compaction): no `compaction_update` / `compaction_summary_chunk` handling or advertised `session.compaction` capability. Agents can still expose a slash command or ordinary messages; context usage updates are supported. |
| Structured advisory notices | [Preview](https://agentclientprotocol.com/rfds/session-notices): no advertised `session.notices` capability or dedicated `notice` update UI. Local client notices are separate from this protocol extension. |
| Remote agent transports | [Proposal in progress](https://agentclientprotocol.com/protocol/v1/transports): agents connect through stdio; no ACP Streamable HTTP or WebSocket connector. HTTP/SSE **MCP server configuration** is already supported and is a different transport boundary. |
| Other proposed extensions | The [RFD index](https://agentclientprotocol.com/rfds/updates) lists draft plan operations, MCP-over-ACP/proxy chains, deletion-aware diffs, next-edit suggestions, configurable providers, end-turn token usage and authentication-state queries. These are not advertised or implemented. Native session fork and subagent sessions are the supported unstable exceptions. |
| Richer terminal presentation | Images/audio are accepted and retained as content where supported, but displayed as labels. There is no inline bitmap display, audio player, editor follow-along view or simultaneous conversation UI. These are product capabilities rather than additional stable ACP methods. |
| Broader live-agent verification | Local fixtures cover protocol behavior. Only the live adapter check described below has been recorded; a registry-wide interoperability matrix remains future validation work. |

## Terminal interaction verification

`TestTerminalWorkflow` runs the built application in a PTY with a Go terminal emulator. It checks what the terminal actually displays, including cursor movement when suggestions shrink, rather than only comparing `View()` strings. The credential-free demo covers the core composer and session workflows; smaller TUI tests cover Unicode references, paste/history, ignored files, queue context, and compact dialogs. A live `codex-acp` 1.13.1 file-edit check was also run on 2026-09-25; other registry adapters have not been exercised live.
