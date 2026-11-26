package client

import (
	"bytes"
	"os"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/gdamore/tcell/v2/terminfo"
)

var (
	syncOutStart = []byte("\x1b[?2026h")
	syncOutEnd   = []byte("\x1b[?2026l")
)

// newScreen creates a tcell screen whose tty strips tcell's unconditional
// per-frame DEC mode 2026 synchronized-output wrapper as well as redundant
// cursor hide/show and DECSCUSR sequences. Both mode 2026 (e.g. in WezTerm on
// WSL2) and per-redraw hide/show toggles can cause a steady native cursor to
// visibly blink on every screen update.
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
// visibility and DECSCUSR style, stripping tcell's per-redraw DEC mode 2026
// wrapper and redundant hide/show/style escapes when the cursor state has not
// changed.
type steadyCursorTty struct {
	tcell.Tty
	hideSeq []byte
	showSeq []byte

	mu            sync.Mutex
	cursorKnown   bool
	cursorVisible bool
	lastStyleSeq  []byte
	dropSyncEnd   bool
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
	w.dropSyncEnd = false
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
// at the end, followed by a standalone sync-output end write. Standalone
// cursor writes from Init/Fini pass through and update tracked state.
func (w *steadyCursorTty) filterLocked(p []byte) []byte {
	if len(w.hideSeq) == 0 || len(w.showSeq) == 0 {
		return p
	}

	// Drop the trailing syncOutEnd paired with a stripped syncOutStart.
	if w.dropSyncEnd {
		w.dropSyncEnd = false
		if bytes.Equal(p, syncOutEnd) {
			return nil
		}
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
	hasSyncOut := bytes.HasPrefix(p, syncOutStart)
	trimmed := p
	if hasSyncOut {
		trimmed = p[len(syncOutStart):]
	}
	if !bytes.HasPrefix(trimmed, w.hideSeq) {
		return p
	}
	rest := trimmed[len(w.hideSeq):]

	// Cursor hidden at end of frame: ... + hideSeq.
	if bytes.HasSuffix(rest, w.hideSeq) {
		body := rest[:len(rest)-len(w.hideSeq)]
		if hasSyncOut {
			w.dropSyncEnd = true
		}
		if w.cursorKnown && !w.cursorVisible {
			return body
		}
		w.cursorKnown = true
		w.cursorVisible = false
		return joinBytes(w.hideSeq, body)
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
	if hasSyncOut {
		w.dropSyncEnd = true
	}
	return joinBytes(leadHide, body, trailShow, trailStyle)
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
