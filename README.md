# cogito

cogito is a tmux-like terminal multiplexer written in Go for Linux. It lets
you run panes and windows of shells (or any TUI) inside one terminal, detach
and re-attach, and drive everything with the mouse.

It is an early, deliberately small implementation: panes, windows, mouse, and
detach/attach — nothing else yet.

## Features

- Multiple **panes** per window, split horizontally/vertically into a binary
  tree; resize by dragging the divider with the mouse or with keys
- Multiple **windows** per session, shown in a status bar (active window
  highlighted, `*` marker, `Z` when zoomed)
- **Detach / reattach**: the session lives in a background server; close the
  terminal and `cogito attach` brings everything back
- **Mouse**: click to focus a pane, click a window in the status bar to
  switch, drag a divider to resize
- **Zoom** a pane to fill the window (with a 1-cell margin so you can tell
  it's zoomed)
- Windows are auto-named after the foreground process
- Multiple clients may attach to the same session; the effective size is the
  smallest attached terminal (tmux-style)

## Build

```sh
go build -o cogito ./cmd/cogito
```

Requires Go 1.22+ and Linux (uses ptys and /proc).

## Usage

```sh
cogito              # start the session server if needed, then attach
cogito attach       # attach to the running session
cogito kill-server  # terminate the session and all panes
```

The server socket lives in `$COGITO_SOCKET_DIR/default`, or
`$XDG_RUNTIME_DIR/cogito-<uid>/default`, or `/tmp/cogito-<uid>/default`.
Server logs are written to `server.log` next to the socket.

### Keys (prefix is `Ctrl-a`)

| Keys              | Action                              |
|-------------------|-------------------------------------|
| `prefix` `%`      | Split left/right                    |
| `prefix` `"`      | Split top/bottom                    |
| `prefix` `c`      | New window                          |
| `prefix` `n` / `p`| Next / previous window              |
| `prefix` `o`      | Cycle to next pane                  |
| `prefix` `x`      | Kill current pane                   |
| `prefix` `z`      | Toggle zoom of current pane         |
| `prefix` `d`      | Detach (session keeps running)      |
| `prefix` `Ctrl-←/→/↑/↓` | Resize pane                    |
| `prefix` `Ctrl-a` | Send a literal `Ctrl-a` to the app  |

Mouse: click a pane to focus it; click a window name in the status bar to
switch; drag a border between panes to resize it. The mouse wheel scrolls
nothing (scrollback is not implemented yet).

When the shell in the last pane of the last window exits, the session ends
and attached clients exit with it.

## Architecture

```
cogito (client)  ──gob over unix socket──▶  cogito server (detached daemon)
   tcell UI                                  owns PTYs, vt10x terminals,
   input + mouse                             layout tree, compositor
```

- `cmd/cogito` — CLI: spawn/attach, `kill-server`
- `internal/client` — tcell front-end: renders frames, encodes keys/mouse
- `internal/server` — session daemon: pane/PTY management, layout, screen
  compositor, prefix-key state machine
- `internal/layout` — binary split tree (rect math, hit-testing, resize)
- `internal/protocol` — gob-encoded messages over the unix socket

The server renders the whole screen (panes + borders + status bar) into a
cell grid, diffs it against the previous frame, and ships only the changed
cells to clients. Clients are thin: they draw cells and forward input.

Environment variables:

- `COGITO_SOCKET_DIR` — override socket directory (useful for testing)
- `COGITO_DEBUG_LOG` — append client-side input events to this file (debug)

## Development

```sh
go test ./...       # unit + integration tests
go vet ./...
go build -o cogito ./cmd/cogito
```

## Limitations

- Linux only
- No scrollback or copy mode
- No config file, no plugins, no theming
- Single unnamed session
- No window renaming, no reordering
