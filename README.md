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
- Creating a pane or window focuses it immediately, so typing goes to the
  new shell
- **Pane numbers** (`prefix` `q`): show a large number in each pane, then
  press that number to focus the pane (tmux-style `display-panes`)
- **Detach / reattach**: the session lives in a background server; close the
  terminal and `cogito attach` brings everything back
- **Mouse**: click to focus a pane, double-click to zoom it, click a window in
  the status bar to switch, drag a divider to resize
- **Zoom** floats a pane above the split layout, centered with at least a 2%
  margin on all four sides of the content area; `prefix` `z` or a
  double-click toggles it
- **Color**: pane output keeps ANSI 16/256-color and 24-bit RGB styling;
  active pane borders, window tabs, and the help overlay are color-coded
- **Interactive help** (`prefix` `?`): categorized, color-coded key cheatsheet
- **Confirmed shutdown** (`prefix` `&`): a modal y/n dialog before ending the
  session and all its panes
- Windows are auto-named after the foreground process
- Multiple clients may attach to the same session; the effective size is the
  smallest attached terminal (tmux-style)

## Build

```sh
make            # builds ./cogito
# or, without make:
go build -o cogito ./cmd/cogito
```

Run `make help` to see targets for tests, vetting, installing, and
cleaning. Set `BINARY=/path/to/cogito` to change the build output.

Requires Go 1.24+ and Linux (uses ptys and /proc).

## Install

```sh
go install github.com/demongiggle/cogito/cmd/cogito@latest
```

`go install` places the binary in `$(go env GOBIN)` (or `$(go env GOPATH)/bin`);
make sure that directory is on your `PATH`. To install a checkout instead:

```sh
go install ./cmd/cogito
```

The `server` subcommand is internal — the CLI spawns it automatically on first run.

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
| `prefix` `\|`     | Split left/right                    |
| `prefix` `-`      | Split top/bottom                    |
| `prefix` `c`      | New window                          |
| `prefix` `n` / `p`| Next / previous window              |
| `prefix` `o`      | Cycle to next pane                  |
| `prefix` `q`      | Show pane numbers / jump to pane   |
| `prefix` `x`      | Kill current pane                   |
| `prefix` `&`      | Shut down session (confirm)          |
| `prefix` `z`      | Toggle floating pane zoom           |
| `prefix` `d`      | Detach (session keeps running)      |
| `prefix` `Ctrl-←/→/↑/↓` | Resize pane (repeatable)       |
| `prefix` `Ctrl-a` | Send a literal `Ctrl-a` to the app  |
| `prefix` `?`      | Show categorized key help           |

The split keys match the divider they draw: `|` separates left/right panes,
`-` separates top/bottom panes.

The Ctrl-arrow resize keys are repeatable: after `prefix` `Ctrl-→`, keep Ctrl
held and press an arrow again within half a second to resize another step
without another prefix. A different Ctrl-arrow works in the same repeat
window; any other key, a new prefix, or the timeout ends it and returns
Ctrl-arrow input to the pane.

`prefix` `q` is tmux-style `display-panes`: a large, color-coded number
appears in each pane and the status bar hints at the range. Press the number
to focus that pane; `Esc`, `q`, `Ctrl-c`, or any other non-number key cancels
(and is swallowed). The overlay is local to the attached client and clears
itself after about two seconds. Numbers are zero-based and follow pane
creation order. With more than ten panes, a first digit that could begin a
two-digit number waits briefly: type the next digit, press Enter to accept
the first digit, or wait for it to be selected automatically. Jumping to a
hidden pane from a zoomed window restores the split layout.

Like tmux's destructive `&` chord, `prefix` `&` opens a modal confirmation
dialog before ending cogito's single session. Press `y` (or Enter) to shut
down every window and pane; `n`, `q`, `Esc`, or `Ctrl-c` cancels. Other keys
are swallowed while the dialog is open. The dialog is local to the attached
client; `cogito kill-server` still shuts down immediately without a prompt.

Press `prefix` `?` for a color-coded cheatsheet of pane, window, layout,
session, and mouse commands. On short terminals, use `↑` / `↓`, `PgUp` / `PgDn`,
or `j` / `k` to scroll; press `Esc`, `q`, or almost any other key to close it.
The menu is local to the attached client.

Mouse: click a pane to focus it; double-click a pane to float it above the
split as a centered zoom overlay, or double-click the floating pane to restore
the layout; click a window name in the status bar to switch; drag a border
between panes to resize it. The mouse wheel scrolls nothing (scrollback is
not implemented yet).

When the shell in the last pane of the last window exits, the session ends
and attached clients exit with it; this shell-driven exit is not prompted.

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

## Manual smoke checklist

Automated coverage lives in `go test ./...` (integration tests drive a real
server over a unix socket). The interactive path is best checked by hand or
under `script(1)`:

```sh
COGITO_SOCKET_DIR=$(mktemp -d) script -qec ./cogito /tmp/cogito.log
```

- [ ] `cogito` starts a session: shell appears, status bar shows `0:<name>*` + clock
- [ ] `printf '\033[31mred\033[0m \033[48;5;22mgreen\033[0m\n'` shows colors in a pane
- [ ] active pane border, active window tab, and `Ctrl-a ?` help are color-coded
- [ ] `Ctrl-a |` splits left/right; `Ctrl-a -` splits top/bottom; the new pane takes focus
- [ ] `Ctrl-a o` cycles panes; focus follows
- [ ] `Ctrl-a q` numbers every pane; `0`/`1` jumps there and clears the overlay
- [ ] `Ctrl-a z` floats the active pane (centered, at least 2% margins, `Z` flag), toggle restores
- [ ] double-click a pane zooms it; double-click the floating pane restores the split
- [ ] click a pane to focus it; click a window name in the status bar to switch windows
- [ ] drag a border: the pane resizes live and stays after detach/reattach
- [ ] `Ctrl-a c` opens and focuses a window; `Ctrl-a n` / `Ctrl-a p` switch; status bar updates
- [ ] type in a pane, `Ctrl-a d` detaches, `cogito attach` restores with the pane intact
- [ ] `Ctrl-a ?` opens the categorized help menu; `Esc` / `q` closes it; pane input resumes
- [ ] `Ctrl-a &` opens shutdown confirmation; `n` / `Esc` cancels; `y` ends the server
- [ ] `Ctrl-a Ctrl-Left/Right/Up/Down` resizes; holding Ctrl and repeating the arrow resizes without another prefix
- [ ] kill the pane (`Ctrl-a x`): window closes; killing the last pane quits everything
- [ ] `cogito kill-server` from another terminal shuts the session down
- [ ] two attached clients: the smaller terminal wins (min-size); closing it grows the other to full size
- [ ] tiny terminal (<40x10): shows "terminal too small" instead of garbage
- [ ] `COGITO_DEBUG_LOG=/tmp/cogito.debug ./cogito` logs key/mouse events for troubleshooting

Mouse events can be injected without a real mouse (SGR 1006): press at
(col,row) is `ESC [ < 0 ; COL ; ROW M`, release is `... m`, motion with the
button held is `... 32 ; COL ; ROW M` (coordinates 1-based).

## Limitations

- Linux only
- No scrollback or copy mode
- No config file, no plugins, no theming
- Single unnamed session
- No window renaming, no reordering
