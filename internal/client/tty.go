package client

import (
	"bytes"
	"os"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/gdamore/tcell/v2/terminfo"
)

var syncOutStart = []byte("\x1b[?2026h")

// newScreen creates a tcell screen whose tty suppresses redundant per-frame
// cursor hide/show and DECSCUSR sequences. Without this, tcell's Show()
// hides and re-shows the native cursor on every redraw, causing a steady
// cursor to visibly blink at the screen refresh rate (1 Hz on the status
// clock tick, faster during active output).
func newScreen() (tcell.Screen, error) {
	if term := os.Getenv("TERM"); term != "" {
		if ti, err := tcell.LookupTerminfo(term); err == nil {
			if devTty, err := tcell.NewDevTty(); err == nil {
				tty := newSteadyCursorTty(devTty, ti)
				if s, err := tcell.NewTerminfoScreenFromTtyTerminfo(tty, ti); err == nil {
					return s, nil
				}
				_ = devTty.Close()
			}
		}
	}
	return tcell.NewScreen()
}

// steadyCursorTty wraps a tcell.Tty to track the host terminal's cursor
// visibility and DECSCUSR style, stripping tcell's unconditional per-redraw
// hide/show and redundant style escapes when the cursor state has not changed.
type steadyCursorTty struct {
	tcell.Tty
	hideSeq []byte
	showSeq []byte

	mu            sync.Mutex
	cursorKnown   bool
	cursorVisible bool
	lastStyleSeq  []byte
}

func newSteadyCursorTty(tty tcell.Tty, ti *terminfo.Terminfo) *steadyCursorTty {
	var hideBuf, showBuf bytes.Buffer
	if ti != nil {
		if ti.HideCursor != "" {
			ti.TPuts(&hideBuf, ti.HideCursor)
		}
		if ti.ShowCursor != "" {
			ti.TPuts(&showBuf, ti.ShowCursor)
		}
	}
	return &steadyCursorTty{
		Tty:     tty,
		hideSeq: hideBuf.Bytes(),
		showSeq: showBuf.Bytes(),
	}
}

func (w *steadyCursorTty) Stop() error {
	w.mu.Lock()
	w.cursorKnown = false
	w.lastStyleSeq = nil
	w.mu.Unlock()
	return w.Tty.Stop()
}

func (w *steadyCursorTty) Write(p []byte) (int, error) {
	w.mu.Lock()
	out := w.filterLocked(p)
	w.mu.Unlock()

	if len(out) == 0 {
		return len(p), nil
	}
	if _, err := w.Tty.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// filterLocked rewrites a single tcell write buffer. tcell's draw() flushes
// an entire frame in one Write call framed by optional sync-output start,
// HideCursor at the beginning, and either HideCursor or ShowCursor+[DECSCUSR]
// at the end. Standalone writes from Init/Fini pass through and update state.
func (w *steadyCursorTty) filterLocked(p []byte) []byte {
	if len(w.hideSeq) == 0 || len(w.showSeq) == 0 {
		return p
	}

	// Standalone control sequences from Init (engage) and Fini (disengage).
	if bytes.Equal(p, w.hideSeq) {
		w.cursorKnown = true
		w.cursorVisible = false
		return p
	}
	if bytes.Equal(p, w.showSeq) {
		w.cursorKnown = true
		w.cursorVisible = true
		return p
	}
	if isDECSCUSR(p) {
		w.lastStyleSeq = append(w.lastStyleSeq[:0], p...)
		return p
	}

	// Frame buffer from tScreen.draw(): [syncOutStart] + hideSeq + ...
	prefixEnd := 0
	if bytes.HasPrefix(p, syncOutStart) {
		prefixEnd = len(syncOutStart)
	}
	if !bytes.HasPrefix(p[prefixEnd:], w.hideSeq) {
		return p
	}
	prefix := p[:prefixEnd]
	rest := p[prefixEnd+len(w.hideSeq):]

	// Cursor hidden at end of frame: ... + hideSeq.
	if bytes.HasSuffix(rest, w.hideSeq) {
		body := rest[:len(rest)-len(w.hideSeq)]
		if w.cursorKnown && !w.cursorVisible {
			return joinBytes(prefix, body)
		}
		w.cursorKnown = true
		w.cursorVisible = false
		return joinBytes(prefix, w.hideSeq, body)
	}

	// Cursor visible at end of frame: ... + showSeq + [DECSCUSR].
	var styleSeq []byte
	withoutStyle := rest
	if len(rest) >= 5 && isDECSCUSR(rest[len(rest)-5:]) {
		styleSeq = rest[len(rest)-5:]
		withoutStyle = rest[:len(rest)-5]
	}
	if !bytes.HasSuffix(withoutStyle, w.showSeq) {
		return p
	}
	body := withoutStyle[:len(withoutStyle)-len(w.showSeq)]

	wasKnown := w.cursorKnown
	wasVisible := w.cursorVisible
	w.cursorKnown = true
	w.cursorVisible = true

	var leadHide, trailShow, trailStyle []byte
	if !wasKnown {
		leadHide = w.hideSeq
	}
	if !wasKnown || !wasVisible {
		trailShow = w.showSeq
	}
	if len(styleSeq) > 0 && !bytes.Equal(styleSeq, w.lastStyleSeq) {
		trailStyle = styleSeq
		w.lastStyleSeq = append(w.lastStyleSeq[:0], styleSeq...)
	}
	return joinBytes(prefix, leadHide, body, trailShow, trailStyle)
}

func isDECSCUSR(b []byte) bool {
	return len(b) == 5 && b[0] == 0x1b && b[1] == '[' && b[2] >= '0' && b[2] <= '6' && b[3] == ' ' && b[4] == 'q'
}

func joinBytes(parts ...[]byte) []byte {
	n := 0
	for _, part := range parts {
		n += len(part)
	}
	if n == 0 {
		return nil
	}
	out := make([]byte, 0, n)
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}
