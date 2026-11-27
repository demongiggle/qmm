# Investigation: Copying with Ctrl+Shift-C (and Why Ctrl-Y Works Instead)

- **Date:** 2026-11-27
- **Environment:** WezTerm / Alacritty / Windows Terminal → WSL2, `$TERM=xterm-256color`,
  `github.com/gdamore/tcell/v2` `v2.13.10`, tmux `3.7c` for comparison
- **Affected components:** `internal/server/settings.go` (`copyKeySequence`),
  `internal/server/actions.go` (mouse selection), `internal/server/copy_mode.go`,
  `internal/client/input.go` (`encodeKey`), `internal/client/client.go`
- **Outcome:** the default copy key is now `Ctrl-Y`; `Ctrl-Shift-C` is still
  selectable but no longer the default.

## Symptoms

With the shipped default (`copy_key = "Ctrl-Shift-C"`), dragging a mouse
selection in qmm and pressing `Ctrl+Shift+C` did nothing: no clipboard update,
no notice, and the selection stayed on screen (or was cancelled and the shell
received `^C`). Switching the setting to `Ctrl-Y` copied immediately.

The same user could press `Ctrl+Shift+C`/`Ctrl+Shift+V` in tmux and get a
working copy/paste, which is what made this look like a qmm regression.

## Findings

### 1. qmm accepts exactly one copy key, matched byte-for-byte

`copyKeySequence()` maps the single `copy_key` setting to one escape sequence,
and both selection paths compare the whole keystroke against it:

- `internal/server/actions.go:499` — pending mouse selection
- `internal/server/copy_mode.go:703` — copy mode
- `internal/server/settings.go:614` — the mapping

| Setting | Bytes on the wire |
| :--- | :--- |
| `Ctrl-Y` | `0x19` |
| `Ctrl-Shift-X` | `\x1b[27;6;120~` |
| `Ctrl-Shift-C` | `\x1b[27;6;99~` |
| `Ctrl-Insert` | `\x1b[2;5~` |

The modified chords use xterm's modifyOtherKeys encoding (`CSI 27 ; modifier ;
codepoint ~`), which the client synthesises in `encodeKey`.

So a non-configured chord can never copy, and there is no second binding.

### 2. `Ctrl-Y` is a single control byte; modified chords are not

`^Y` (`0x19`) is delivered by every tty unchanged: no terminal binds it, no
protocol negotiation is required. Anything of the form `Ctrl-Shift-<letter>`
needs the terminal to (a) not consume it and (b) tell the app that Shift was
held — a much longer chain with two common failure points.

### 3. Failure point A: the host terminal claims the chord

WezTerm, Alacritty, Windows Terminal, GNOME Terminal, Konsole and kitty all
bind `Ctrl+Shift+C` to their own *Copy* (and `Ctrl+Shift+V` to *Paste*) by
default. The terminal resolves the key itself and writes **nothing** to the
pty, so qmm's client never sees an `EventKey` and the server never sees an
`Input` message. `Ctrl+Shift+V` keeps appearing to work because paste is a
terminal→pty direction: the terminal pastes its clipboard into whatever is
focused, including a qmm pane.

### 4. Failure point B: Shift is dropped without an extended-key protocol

If the chord does reach qmm's tty, whether Shift survives depends on the
keyboard protocol in force. For `XTermLike` terminfo, tcell enables all three
at `Init` (`tscreen.go:322-336`, written at `tscreen.go:1208`):

```text
\x1b[>4;2m   xterm modifyOtherKeys=2
\x1b[>1u     kitty CSI-u
\x1b[?9001h  win32-input-mode
```

Then `Ctrl+Shift+C` arrives either as `KeyCtrlC` + `ModShift` or as
`KeyRune('c')` + `ModCtrl|ModShift`, and `encodeKey`
(`internal/client/input.go:86-113`) re-encodes it as `\x1b[27;6;99~`, which is
what `copyKeySequence` expects. That path is covered by
`internal/client/input_test.go:27-30`.

Where the enable is ignored, Shift is silently dropped and the pty receives
plain `0x03`, which qmm forwards to the pane — observed as an unexpected `^C`,
never a copy. `XTermLike` is false for some common `$TERM`s (probe below), and
an intermediate multiplexer can also refuse to forward the enable.

### 5. Why tmux looked like it supported the chord

Stock tmux has no such binding:

```console
$ tmux -L qmmprobe -f /dev/null start-server \; list-keys | grep -E '\bC-S-|\bS-C-'
$ tmux -L qmmprobe -f /dev/null list-keys | wc -l
275
```

`Ctrl+Shift+C`/`Ctrl+Shift+V` in tmux are handled by the **terminal**, copying
the *terminal's* selection — which exists in tmux only when tmux is not holding
the mouse (mouse mode off, or a `Shift`-modified drag, or terminal
copy-on-select). tmux itself is bypassed. With `set -g mouse on` a plain drag
belongs to tmux, and a terminal-level Copy then has nothing to copy, which is
the same position qmm is in.

Also note `tmux show-options -g extended-keys` reports `off` on tmux 3.7c, so
a qmm client running inside tmux asks the outer terminal for extended keys
through tmux and gets nothing back.

### 6. Terminfo dependency

```console
$ go run /tmp/ti_probe.go          # tcell.LookupTerminfo(...).XTermLike
xterm-256color   XTermLike=true
tmux-256color    XTermLike=true
alacritty        XTermLike=true
xterm-kitty      XTermLike=true
screen-256color  XTermLike=false
linux            XTermLike=false
```

with

```go
package main

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

func main() {
	for _, name := range []string{"xterm-256color", "tmux-256color", "screen-256color", "alacritty", "xterm-kitty", "linux"} {
		ti, err := tcell.LookupTerminfo(name)
		if err != nil {
			fmt.Printf("%-16s lookup error: %v\n", name, err)
			continue
		}
		fmt.Printf("%-16s XTermLike=%v\n", name, ti.XTermLike)
	}
}
```

`XTermLike=false` ⇒ no modifyOtherKeys/CSI-u request ⇒ `Ctrl-Shift-<letter>`
collapses to the unmodified control byte. So even a terminal that forwards the
chord cannot preserve Shift under `screen-256color` or `linux`.

### 7. Why we do not "let the terminal select while qmm keeps its gestures"

DEC mouse tracking is exclusive, and enabling any of it suppresses the
terminal's local selection (Shift being the only override). tcell writes
`CSI ?1000h ?1002h ?1006h` from `EnableMouse()`
(`internal/client/client.go:50`, `tscreen.go:807-828`).

| Mode | App receives | Terminal may select |
| :--- | :--- | :--- |
| none | nothing | yes |
| `?1000h` | press + release | no |
| `?1002h` | press/release + drag motion (qmm) | no |
| `?1003h` | all motion | no |

There is no mode that reports clicks while the terminal keeps drag-selection,
and `?1000h` would not help anyway: the terminal still refuses to select and
qmm would lose live drag positions. The only per-gesture split available is
the terminal's own `Shift` override, and its selection lifecycle (highlight,
clearing on click, scroll) is invisible to qmm, so qmm cannot coordinate it.

qmm therefore needs its own copy key; the terminal does not have to own the
selection for the clipboard to work, because copy goes out as OSC 52
(`internal/client/tty.go`, `writeClipboard`).

## Resolution

1. **Default changed to `Ctrl-Y`** (`copyKeyNames` in
   `internal/server/settings.go` is now ordered `Ctrl-Y`, `Ctrl-Shift-X`,
   `Ctrl-Shift-C`, `Ctrl-Insert`; the first entry is the default because
   `settingCopyKey` uses `Default: 0`). `copyKeySequence` returns `0x19` as its
   fallback for nil/unknown settings, so the fallback and the default agree.
   Existing `config.toml` files are unaffected: settings are persisted as text
   (`copy_key = "Ctrl-Shift-C"` still resolves after the reorder) and are
   matched by name on load.
2. **README** documents `Ctrl-Y` as the default, explains why modified chords
   fail, and points at this file.
3. **Tests**: `TestConfiguredCopyKeyCopiesAndClosesSelection` now exercises the
   default as `Ctrl-Y`, `TestCopyKeyConsumesPendingPrefix`,
   `TestCopyKeyWithoutSelectionDoesNotCopy`,
   `TestCopyReturnsToTheLivePaneView` and the mouse-selection integration test
   send `0x19`, and `config_test.go` asserts the default sequence plus a
   `Ctrl-Shift-X` round trip.
4. `Ctrl-Shift-C` stays available for terminals that do forward it.

## Workarounds kept for users (and future work)

- `y`, `Enter`, and `Ctrl-c`-with-a-selection always copy in copy mode; a mouse
  selection copies with the configured key only.
- Shift+drag hands the drag to the terminal for a native selection and native
  copy chord (visible screen only — qmm runs on the alternate screen).
- A terminal can be configured to emit `0x19` for `Ctrl+Shift+C`, making the
  familiar chord work with `copy_key = "Ctrl-Y"`:

  ```lua
  -- WezTerm: config.keys takes precedence over the built-in Copy binding
  { key = 'C', mods = 'CTRL|SHIFT', action = wezterm.action.SendString('\x19') }
  ```
  ```toml
  # Alacritty
  [[keyboard.bindings]]
  key = "C"
  mods = "Control|Shift"
  chars = "\u0019"
  ```
  ```json
  // Windows Terminal actions
  { "keys": "ctrl+shift+c", "command": { "action": "sendInput", "input": "\u0019" } }
  ```
- Not implemented, deliberately: a runtime mouse on/off toggle (tmux parity)
  and accepting several copy keys at once.

## Repro / diagnostic recipe

```console
$ QMM_DEBUG_LOG=/tmp/qmm.log qmm attach   # drag a selection, press the chord
$ grep -E 'key:|send Input' /tmp/qmm.log
```

Interpretation:

- no `key:` line at all → the terminal consumed the chord (failure point A);
- `key: key=… rune='c' mods=Ctrl|Shift` followed by
  `send Input: 1b 5b 32 37 3b 36 3b 39 39 7e` → qmm received the chord and it
  matches `copy_key = "Ctrl-Shift-C"`;
- `send Input: 03` → Shift was dropped by the terminal/`$TERM` (failure point B).

## Takeaways for future terminal-input bugs

- **Copy is terminal-side; paste is terminal→pty.** A working paste chord says
  nothing about whether the app ever sees the corresponding copy chord.
- **A chord doing something in another multiplexer can be the host terminal,
  not the multiplexer.** Verify with `tmux -f /dev/null list-keys` (or the
  equivalent) before blaming the app.
- **Check whether the terminal forwarded any byte** before touching key
  encoding: `QMM_DEBUG_LOG` distinguishes "never arrived" from "arrived
  mangled" in seconds.
- **Prefer single control bytes** for app-level bindings that must survive
  every terminal, multiplexer and `$TERM`; modified-letter chords depend on
  extended-key negotiation all the way down the stack.
- `XTermLike` in the terminfo decides whether tcell even asks for extended
  keys; probe it with `tcell.LookupTerminfo` rather than guessing.
