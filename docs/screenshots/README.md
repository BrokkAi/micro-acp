# README screenshots

These PNGs were captured on 2026-10-05 from a development build of the running
application. It was built with plain `go build`, so the version reads `dev`.
`agent-selection.png` uses the normal `micro-acp` startup with the live
registry loaded and Codex highlighted, before connecting. Agent versions and
the catalog reflect the capture date. The other images use `micro-acp --demo`:
its built-in agent communicates with the client over ACP and supplies the
sample responses, settings, and dialogs.

The binary ran in a detached 96-column, 32-row tmux 3.5a pane with the
environment shown below. Each screen was captured with
`tmux capture-pane -p -e` and replayed into xterm.js 6.0.0, with the terminal
cursor placed where tmux reported it. Chromium captured the terminal at 2× pixel
density, using DejaVu Sans Mono at 16 px with 1.35 line spacing, foreground
`#DDE4EE`, and background `#111820`. The tmux window style set the same
colors, so tmux reported that background to micro-acp, which chose its dark
palette and derived its tints from it. The
captures include all occupied terminal rows with 24 px of padding. Unused rows
and the terminal emulator's scrollbar are outside the captured presentation;
application text, layout, and colors are unchanged.

To recreate the screens, build with `go build -o bin/micro-acp .` and start a
fresh process for each capture from the repository root. Use an isolated state
directory:

```sh
capture_state=$(mktemp -d)
env -u NO_COLOR TERM=xterm-256color COLORTERM=truecolor \
  MICRO_ACP_HOME="$capture_state" ./bin/micro-acp --demo
```

For agent selection, omit `--demo` and wait for the registry to finish loading.
This requires network access but does not require an agent login.

| Image | Interaction after startup finishes |
| --- | --- |
| `agent-selection.png` | In the normal startup picker, press Down until Codex is highlighted; leave the search empty and do not press Enter. |
| `conversation.png` | Send `Show me around micro-acp.`, wait for the response to finish, then type `Review @internal/client/` without submitting. |
| `settings.png` | Run `/config`. |
| `permission.png` | Send `permission`; leave Cancel selected. |
| `input.png` | Send `form`, then press Enter to accept the default name and show the Greeting field. |

Quit with `/quit` after dismissing any open dialog. Remove the temporary state
directory when finished. Real agents may offer different settings, permission
choices, and form fields.
