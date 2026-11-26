# Investigation: Steady Native Cursor Blinks on Every Screen Refresh (DEC Mode 2026)

- **Date:** 2026-11-26
- **Environment:** WezTerm (Windows) → WSL2 (ConPTY), outer `$TERM=xterm-256color`, `github.com/gdamore/tcell/v2` `v2.13.10`
- **Affected components:** `internal/client/tty.go`, `internal/client/render.go`, `internal/client/client.go`, `internal/server/session.go`

## Symptoms

After switching `qmm` from a software-rendered caret to the host terminal's native cursor via `DECSCUSR` (commit `d1c9d9a`):

1. With **Cursor blink** set to **Off** (`\x1b[2 q`, steady block), the cursor still blinked at ~1 Hz while idle—strictly in lockstep with the status bar clock ticking.
2. When the screen refreshed faster (e.g., `watch ls -l`, typing, or animated spinners), the cursor blinked faster, matching the screen refresh rate.
3. Disabling the **Status bar clock** stopped the idle blink completely, but blinking immediately returned whenever pane output updated the screen.
4. Running `tmux` (`3.7c`) in the exact same WezTerm + WSL2 + `$TERM=xterm-256color` environment did **not** exhibit the blink.

## False Leads (and Useful Side Improvements)

Before finding the true trigger, several plausible hypotheses were tested and ruled out:

1. **Blinking text cell underneath the cursor (`8e6e7da`):**
   Clearing `StyleBlink` (SGR 5) on the cell beneath a steady caret did not stop the refresh-coupled blink.
2. **`tcell`'s per-frame `HideCursor` / `ShowCursor` / `DECSCUSR` toggles (`e22e626`):**
   On every `screen.Show()`, `tcell`'s `tScreen.draw()` unconditionally emits:
   - `\x1b[?2026h` (DEC mode 2026 synchronized-output start, whenever `ti.XTermLike` is true)
   - `\x1b[?25l` (`HideCursor`)
   - dirty cells
   - `TGoto(x, y)` + `\x1b[?12l\x1b[?25h` (`ShowCursor`) + `\x1b[2 q` (`DECSCUSR`)
   - `\x1b[?2026l` (in a second `Write` call via `defer t.endBuffering()`)

   Wrapping `tcell.Tty` (`steadyCursorTty`) to strip redundant `HideCursor`, `ShowCursor`, and `DECSCUSR` sequences, skipping `screen.Show()` on no-op frames, coalescing `\x1b[?2026h`…`\x1b[?2026l` into a single `Write`, and draining `bufio.Reader` in `pane.readLoop` before calling `onOutput()` were all real improvements—yet the blink **still persisted** on every clock tick.

## How We Isolated the Root Cause

### 1. Capture the exact bytes written to the TTY

Logging raw `steadyCursorTty.Write` output showed that every 1-second clock tick emitted **zero** `\x1b[?25l`, `\x1b[?25h`, `\x1b[?12l`, or `\x1b[2 q` escapes—only a single atomic write:

```text
\x1b[?2026h\x1b[49;211H\x1b(B\x1b[m\x1b[38;5;248;48;5;236m6\x1b[1;6H\x1b[?2026l
```

### 2. Compare against `tmux 3.7c` in the same environment

Capturing `tmux 3.7c` on a 1-second status clock tick showed:

```text
\x1b[?25l\x1b[30m\x1b[42m\x1b[24;1H[0] 0:sh*\x1b[63X\x1b[63C11:45:14\x1b(B\x1b[m\x1b[?12l\x1b[?25h\x1b[1;3H
```

Key difference: `tmux` does **not** emit DEC mode 2026 (`\x1b[?2026h` / `\x1b[?2026l`) unless explicitly enabled via `terminal-features`.

### 3. Minimal standalone `printf` reproducers (outside `qmm`)

Running three 2.5-second shell one-liners directly in WezTerm + WSL2 isolated the exact sequence:

- **Test A** (`?2026h/l`, no `?25l/h`, steady `2 q`) — **BLINKED**:
  ```sh
  printf '\e[?1049h\e[H\e[?12l\e[?25h\e[2 qhello'; for i in 1 2 3 4 5; do sleep 0.5; printf '\e[?2026h\e[20;40H%d\e[1;6H\e[?2026l' "$i"; done; printf '\e[0 q\e[?1049l'
  ```
- **Test B** (identical to Test A, **without** `?2026h/l`) — **STEADY (no blink)**:
  ```sh
  printf '\e[?1049h\e[H\e[?12l\e[?25h\e[2 qhello'; for i in 1 2 3 4 5; do sleep 0.5; printf '\e[20;40H%d\e[1;6H' "$i"; done; printf '\e[0 q\e[?1049l'
  ```
- **Test C** (`tmux`-style `?25l/h`, no `?2026h/l`, no `2 q`) — **STEADY (no blink)**:
  ```sh
  printf '\e[?1049h\e[H\e[?12l\e[?25hhello'; for i in 1 2 3 4 5; do sleep 0.5; printf '\e[?25l\e[20;40H%d\e[?12l\e[?25h\e[1;6H' "$i"; done; printf '\e[?1049l'
  ```

Only Test A blinked, proving beyond doubt that **DEC mode 2026 (`\x1b[?2026h` / `\x1b[?2026l`)** was the trigger.

## Root Cause

A combination of two behaviors:

1. **WezTerm / WSL2 (ConPTY) rendering bug:** Wrapping updates in DEC mode 2026 (`\x1b[?2026h` … `\x1b[?2026l`) causes a steady native cursor (`\x1b[2 q`) to visibly blink on every wrapped frame, even when cursor visibility (`?25l`/`?25h`) is never toggled.
2. **`tcell` unconditional mode 2026 emission:** In `tcell/v2` (`tscreen.go:314-320`), `tScreen` unconditionally sets `startSyncOut = "\x1b[?2026h"` and `endSyncOut = "\x1b[?2026l"` for any `XTermLike` terminfo without querying terminal support via `DECRQM` (`\x1b[?2026$p`) or providing an API to disable it.

## Fix

1. **`internal/client/tty.go` (`steadyCursorTty`):**
   - Strips `tcell`'s paired `\x1b[?2026h` prefix and trailing `\x1b[?2026l` write on every `draw()` frame.
   - Continues to track cursor visibility and active `DECSCUSR` style so redundant per-frame `HideCursor`, `ShowCursor`, and `DECSCUSR` escapes are suppressed, emitting each incremental update in a single `Write` syscall (matching Test B).
2. **`internal/client/render.go` & `internal/client/client.go`:**
   - `applyFrame` returns `bool` indicating whether any visible cell or cursor state changed, skipping `screen.Show()` on no-op frames.
3. **`internal/server/session.go` (`pane.readLoop`):**
   - Drains `br.Buffered() == 0` before calling `onOutput()`, preventing full-screen apps (like `watch` / ncurses) that hide, redraw, and re-show the cursor within one PTY write from broadcasting intermediate `CursorVisible: false` frames.

## Takeaways for Future Terminal Bugs

- **Always capture the actual raw TTY bytes** before assuming which escape sequence is misbehaving.
- **Audit `tcell`'s implicit private modes** (`?2026h/l`, `?9001h/l`, `>4;2m`, `>1u`, etc.)—`tcell` enables several xterm extensions unconditionally whenever `XTermLike` is true.
- **Isolate terminal/ConPTY quirks with minimal `printf` loops** outside `qmm` to separate app logic, `tcell` behavior, and terminal/ConPTY rendering bugs in seconds.
