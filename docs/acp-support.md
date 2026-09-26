# ACP support

Target: stable ACP v1 as represented by `github.com/BrokkAi/acp-go`, pinned to commit `5b2c77c673e0` (v0.10.0 plus the cancellation transport fix). All 25 methods in that stable schema have a client path or are handled by the SDK transport. Optional operations depend on negotiated agent capabilities. This is implementation and fixture coverage, not certification against every registry agent.

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
| `session/prompt` | Multiline composer, streamed response, rich content, agent slash commands. | Lifecycle, configuration and rich content tests. |
| `session/cancel` | Esc/Ctrl+C stops a turn, cancels pending interactions, waits for completion; disconnects unresponsive processes. | Permission/cancellation tests. |
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
| `session_info_update` | Updates title and timestamp. |
| `usage_update` | Stores used/capacity and reported cost; the status line displays the percentage of context remaining. |

Prompts can carry text, resource links, images, audio, embedded text resources and embedded binary resources. Capability checks happen before sending. Full content blocks persist in local session files. The TUI renders text/resources and labels images, audio and binary data; it does not render bitmap images or play audio. Tool content supports text/resources, diffs and terminal references. `TestRichContentRoundTripAndPersistence` verifies these survive the actual stdio transport and disk storage.

Form fields support the restricted flat schema: strings, integers, numbers, booleans, string enums, titled choices and multiple choices, with defaults and constraints. A raw overlay preserves `requestedSchema`, `url` and `elicitationId`, which v0.10.0's generated mode payloads omit. Transport, schema validation types and response unions still come from `acp-go`. See [ACP elicitation](https://agentclientprotocol.com/protocol/v1/elicitation).

## Extensions and boundaries

- `session/fork`: implemented through the SDK's unstable v1 schema only when advertised. Replayed history replaces the inherited transcript; agents that do not replay retain the saved history. Tested by `TestNativeForkUsesAdvertisedCapability` and `TestNativeForkReplayReplacesInheritedHistory`. `/fork --context` is a separately labeled text-context fallback.
- MCP configuration: stdio, HTTP and SSE definitions are forwarded during session operations. The agent connects to and operates those servers. This client is not an MCP server or an MCP-over-ACP proxy.
- Additional directories: passed only through supported session operations, restored with saved sessions, and included in client callback routing.
- Unknown notifications are ignored; unsupported requests receive JSON-RPC method-not-found. No editor document synchronization, inline completion, next-edit prediction or experimental provider-management capabilities are advertised.
- Draft ACP v2, arbitrary third-party extensions, inline image display and audio playback are outside this implementation.
- Only one active conversation per connection is presented. Session switches release client terminal processes. Sessions and custom agents can be switched from the UI.
- Registry freshness concerns discovery and the next agent launch. An already-running process is not upgraded during a turn.

Run `make test` for the race-enabled suite and `make vet` for static checks. Tests use local subprocess agents and loopback registry servers; they do not use live model accounts. `--demo` exercises streaming, session history, settings, permission requests, forms and cancellation without credentials.

## Terminal interaction verification

`TestTerminalWorkflow` runs the built application in a PTY with a Go terminal emulator. It checks what the terminal actually displays, including cursor movement when suggestions shrink, rather than only comparing `View()` strings. The credential-free demo covers the core composer and session workflows; smaller TUI tests cover Unicode references, paste/history, ignored files, queue context, and compact dialogs. A live `codex-acp` 1.13.1 file-edit check was also run on 2026-09-25; other registry adapters have not been exercised live.
