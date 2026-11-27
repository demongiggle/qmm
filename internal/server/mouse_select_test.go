package server

import (
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

func writePaneText(t *testing.T, s *Server, paneID int, text string) {
	t.Helper()
	p := s.sess.panes[paneID]
	if p == nil {
		t.Fatalf("pane %d missing", paneID)
	}
	if _, err := p.vt.Write([]byte(text)); err != nil {
		t.Fatalf("write pane: %v", err)
	}
}

// startMouseSelection opens a mouse selection on the active pane and returns
// it, asserting that no copy mode was entered on the way.
func startMouseSelection(t *testing.T, s *Server, c *clientConn, x, y int) *selectionState {
	t.Helper()
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: x, Y: y}})
	if c.copyMode != nil {
		t.Fatal("a mouse selection must not open copy mode")
	}
	if c.selection == nil {
		t.Fatal("ActionSelectStart did not create a selection")
	}
	return c.selection
}

func TestSelectStartSnapshotsPaneWithoutCopyMode(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	sel := startMouseSelection(t, s, c, 0, 0)
	if sel.paneID != 1 {
		t.Fatalf("paneID = %d, want 1", sel.paneID)
	}
	if want := (layout.Rect{X: 0, Y: 0, W: 40, H: 23}); sel.rect != want {
		t.Fatalf("rect = %+v, want %+v", sel.rect, want)
	}
	// The pane is 23 rows tall, so the visible screen fills the snapshot and
	// the viewport sits at the newest line.
	if len(sel.lines) != 23 {
		t.Fatalf("snapshot lines = %d, want 23", len(sel.lines))
	}
	if sel.top != sel.maxTop() {
		t.Fatalf("top = %d, want bottom of buffer %d", sel.top, sel.maxTop())
	}
	if sel.anchor != (copyPoint{line: 0, col: 0}) {
		t.Fatalf("anchor = %+v, want {0 0}", sel.anchor)
	}
	if c.frame == nil {
		t.Fatal("selection did not render a frame")
	}
	if c.frame.CursorVisible {
		t.Fatal("selection view kept the live caret visible")
	}
}

func TestSelectUpdateExtendsSelection(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	sel := startMouseSelection(t, s, c, 0, 0)
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 4, Y: 0}})

	if got := sel.cursor; got != (copyPoint{line: 0, col: 4}) {
		t.Fatalf("cursor = %+v, want {0 4}", got)
	}
	if got := sel.selectedText(); got != "hello" {
		t.Fatalf("selected text = %q, want %q", got, "hello")
	}
}

func TestSelectUpdateClampsToTheSelectedPane(t *testing.T) {
	s, c := newHelpServer(80, 24)
	sel := startMouseSelection(t, s, c, 0, 0)

	// A drag past the pane's bottom-right corner clamps to its last cell and
	// must not wander into the neighbouring pane.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 500, Y: 500}})
	if got, want := sel.cursor.line, len(sel.lines)-1; got != want {
		t.Fatalf("cursor.line = %d, want %d", got, want)
	}
	if got, want := sel.cursor.col, 38; got != want {
		t.Fatalf("cursor.col = %d, want %d", got, want)
	}
}

func TestSelectStartTargetsThePaneUnderThePointer(t *testing.T) {
	s, c := newHelpServer(80, 24)
	if got := s.activeWindow().activePane; got != 1 {
		t.Fatalf("active pane = %d, want 1", got)
	}
	// No ActionFocus first: the pointer position alone has to pick pane 2
	// (which starts at column 41).
	sel := startMouseSelection(t, s, c, 45, 5)
	if sel.paneID != 2 {
		t.Fatalf("paneID = %d, want the pane under the pointer (2)", sel.paneID)
	}
	if want := (copyPoint{line: sel.top + 5, col: 4}); sel.anchor != want {
		t.Fatalf("anchor = %+v, want %+v", sel.anchor, want)
	}
}

func TestSelectUpdateWithoutStartIsIgnored(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 3, Y: 3}})
	if c.selection != nil {
		t.Fatal("select update without start created a selection")
	}
	if c.copyMode != nil {
		t.Fatal("select update without start opened copy mode")
	}
}

func TestSelectUpdateIgnoredForKeyboardCopyMode(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(10, "row %02d"))
	before := st.cursor
	s.handleSelectUpdate(c, 3, 3)
	if st.cursor != before {
		t.Fatalf("mouse update moved keyboard copy mode cursor: %+v -> %+v", before, st.cursor)
	}
}

func TestSelectStartWhileKeyboardCopyModeOpenReanchors(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(10, "row %02d"))
	st.cursor = copyPoint{line: 9, col: 4}
	st.anchor = st.cursor
	s.handleSelectStart(c, 2, 1)
	if c.selection != nil {
		t.Fatal("a drag inside copy mode must stay in copy mode")
	}
	if !st.mouseDrag {
		t.Fatal("drag flag not set on re-anchor")
	}
	want := copyPoint{line: st.top + 1, col: 2}
	if st.anchor != want {
		t.Fatalf("anchor = %+v, want %+v", st.anchor, want)
	}
	if st.cursor != want {
		t.Fatalf("cursor = %+v, want %+v", st.cursor, want)
	}
	if st.selKind != selectionChar {
		t.Fatalf("selKind = %v, want selectionChar", st.selKind)
	}
}

func TestConfiguredCopyKeyCopiesAndClosesSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		bytes string
		idx   int
	}{
		{"default ctrl-y", "Ctrl-Y", "\x19", 0},
		{"ctrl-shift-x", "Ctrl-Shift-X", "\x1b[27;6;120~", 1},
		{"ctrl-shift-c", "Ctrl-Shift-C", "\x1b[27;6;99~", 2},
		{"ctrl-insert", "Ctrl-Insert", "\x1b[2;5~", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			writePaneText(t, s, 1, "clip me")
			if tc.idx != 0 {
				if !s.sess.opts().setByID(settingCopyKey, tc.idx) {
					t.Fatal("failed to set copy key setting")
				}
			}
			startMouseSelection(t, s, c, 0, 0)
			s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 6, Y: 0}})

			s.handleInput(c, []byte(tc.bytes))

			if c.selection != nil {
				t.Fatal("copy key did not clear the selection")
			}
			if c.copyMode != nil {
				t.Fatal("copy key opened copy mode")
			}
			clip, ok := queuedClipboard(c)
			if !ok {
				t.Fatal("copy key did not enqueue a Clipboard message")
			}
			if clip.Text != "clip me" {
				t.Fatalf("clipboard = %q, want %q", clip.Text, "clip me")
			}
			if s.pasteBuffer != "clip me" {
				t.Fatalf("paste buffer = %q, want %q", s.pasteBuffer, "clip me")
			}
		})
	}
}

func TestCopyKeyWithoutSelectionDoesNotCopy(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "clip me")

	s.handleInput(c, []byte("\x19"))
	if _, ok := queuedClipboard(c); ok {
		t.Fatal("copy key copied without a selection")
	}
	if c.selection != nil {
		t.Fatal("copy key created a selection")
	}
}

func TestOrdinaryKeysReturnToTheLivePane(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"printable", "x"},
		{"esc", "\x1b"},
		{"ctrl-c", "\x03"},
		{"enter", "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			writePaneText(t, s, 1, "hello world")
			startMouseSelection(t, s, c, 0, 0)

			s.handleInput(c, []byte(tc.key))

			if c.selection != nil {
				t.Fatal("key did not return to the live pane")
			}
			if _, ok := queuedClipboard(c); ok {
				t.Fatal("a plain key copied the selection")
			}
			if c.copyMode != nil {
				t.Fatal("a plain key opened copy mode")
			}
		})
	}
}

func TestCtrlCQuitsKeyboardCopyModeWithoutSelection(t *testing.T) {
	s, c, _ := newCopyModeFixture(80, 24, linesOf(10, "row %02d"))
	// No v/V/Ctrl-v: selKind is selectionNone, so Ctrl-C must quit
	// without copying (same as q/Esc).
	s.handleCopyModeInput(c, []byte("\x03"))
	if c.copyMode != nil {
		t.Fatal("Ctrl-C did not close empty copy mode")
	}
	if _, ok := queuedClipboard(c); ok {
		t.Fatal("Ctrl-C copied without an active selection")
	}
}

func TestCtrlCCopiesKeyboardSelection(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, []copyLine{lineOf("alpha"), lineOf("bravo")})
	st.gotoLine(0)
	copyKeys(s, c, "v", "l")
	s.handleCopyModeInput(c, []byte("\x03"))
	if c.copyMode != nil {
		t.Fatal("Ctrl-C did not close copy mode after keyboard selection")
	}
	clip, ok := queuedClipboard(c)
	if !ok {
		t.Fatal("Ctrl-C did not copy keyboard selection")
	}
	if clip.Text != "al" {
		t.Fatalf("clipboard = %q, want %q", clip.Text, "al")
	}
}

func TestWheelExtendsMouseSelectionIntoScrollback(t *testing.T) {
	s, c := newHelpServer(80, 24)
	sel := &selectionState{
		paneID: 1,
		lines:  linesOf(40, "row %02d"),
		rect:   layout.Rect{X: 0, Y: 0, W: 80, H: 23},
	}
	sel.top = sel.maxTop()
	if sel.top == 0 {
		t.Fatal("fixture expected a scrolled-down viewport")
	}
	sel.anchor = copyPoint{line: 25, col: 0}
	sel.cursor = sel.anchor
	c.selection = sel

	s.handleWheel(c, -1) // wheel up one notch

	wantTop := sel.maxTop() - wheelStepLines
	if sel.top != wantTop {
		t.Fatalf("top = %d, want %d", sel.top, wantTop)
	}
	if got, want := sel.cursor.line, 25-wheelStepLines; got != want {
		t.Fatalf("cursor.line = %d, want it to ride the view upward into scrollback", got)
	}
	if !sel.inSelection(sel.cursor.line, 0) || !sel.inSelection(25, 0) {
		t.Fatalf("selection bounds do not cover the extended range: %+v..%+v", sel.anchor, sel.cursor)
	}
	if c.copyMode != nil {
		t.Fatal("wheel with a selection must not enter copy mode")
	}
}

func TestWheelDownAtNewestLineEndsSelection(t *testing.T) {
	s, c := newHelpServer(80, 24)
	sel := &selectionState{
		paneID: 1,
		lines:  linesOf(40, "row %02d"),
		rect:   layout.Rect{X: 0, Y: 0, W: 80, H: 23},
	}
	sel.top = sel.maxTop()
	sel.anchor = copyPoint{line: 30, col: 0}
	sel.cursor = sel.anchor
	c.selection = sel

	s.handleWheel(c, 1)
	if c.selection != nil {
		t.Fatal("wheel-down at the newest line did not end the selection")
	}
}

func TestMouseSelectionKeepsLiveStatusBarAndCursorHidden(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	startMouseSelection(t, s, c, 0, 0)
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 4, Y: 0}})
	if c.frame == nil {
		t.Fatal("no frame after selection update")
	}
	text := frameText(*c.frame)
	if strings.Contains(text, "[copy mode]") {
		t.Fatal("a mouse selection rendered the copy-mode hint bar")
	}
	if !strings.Contains(text, "0:zsh") {
		t.Fatal("a mouse selection lost the normal status bar")
	}
	if len(c.frame.Tabs) == 0 {
		t.Fatal("a mouse selection lost the status bar tab hit zones")
	}
	if c.frame.CursorVisible {
		t.Fatal("a frozen selection view must hide the pane caret")
	}
	// The pane snapshot is drawn in place: the pane is still a hit target.
	if len(c.frame.Panes) != 2 {
		t.Fatalf("frame has %d pane rects, want 2", len(c.frame.Panes))
	}
}

func TestMouseSelectionHighlightStaysInItsPane(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	startMouseSelection(t, s, c, 0, 0)
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 4, Y: 0}})
	if c.frame == nil {
		t.Fatal("no frame after selection update")
	}
	highlighted := 0
	for _, cell := range c.frame.Cells {
		if cell.BG != colCopySelectionBG {
			continue
		}
		highlighted++
		if cell.Col >= 40 {
			t.Fatalf("highlight leaked into the second pane at %d,%d", cell.Col, cell.Row)
		}
	}
	if highlighted != 5 {
		t.Fatalf("highlighted cells = %d, want 5 (\"hello\")", highlighted)
	}
}

func TestMouseSelectionShowsFrozenSnapshot(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "first")

	startMouseSelection(t, s, c, 0, 0)
	// Output keeps arriving in the live pane; the selection view must not
	// scroll out from under the highlight.
	writePaneText(t, s, 1, "\r\nsecond")
	s.broadcast()

	if c.frame == nil {
		t.Fatal("no frame after pane output")
	}
	text := frameText(*c.frame)
	if !strings.Contains(text, "first") {
		t.Fatal("selection view lost the snapshotted text")
	}
	if strings.Contains(text, "second") {
		t.Fatal("selection view showed live output instead of its snapshot")
	}
}

func TestCopyReturnsToTheLivePaneView(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "before")

	startMouseSelection(t, s, c, 0, 0)
	writePaneText(t, s, 1, "\r\nAFTER")
	s.broadcast()

	s.handleInput(c, []byte("\x19"))
	if c.selection != nil {
		t.Fatal("copy did not clear the selection")
	}
	if c.frame == nil {
		t.Fatal("no frame after copy")
	}
	text := frameText(*c.frame)
	if !strings.Contains(text, "AFTER") {
		t.Fatal("live pane output did not return after copying")
	}
	if !c.frame.CursorVisible {
		t.Fatal("live pane caret did not return after copying")
	}
}

func TestMouseSelectionDoesNotBlockPaneActions(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	startMouseSelection(t, s, c, 0, 0)
	// A click on the other pane focuses it and drops the selection instead of
	// being swallowed by a modal overlay.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: 45, Y: 5}})
	if c.selection != nil {
		t.Fatal("focus click did not clear the selection")
	}
	if got := s.activeWindow().activePane; got != 2 {
		t.Fatalf("active pane = %d, want 2", got)
	}
}

func TestMouseSelectionDoesNotBlockDoubleClickZoom(t *testing.T) {
	s, c := newHelpServer(80, 24)
	startMouseSelection(t, s, c, 0, 0)

	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 1}})
	if c.selection != nil {
		t.Fatal("double-click zoom did not clear the selection")
	}
	if !s.activeWindow().zoomed {
		t.Fatal("double-click zoom was swallowed by the selection")
	}
}

func TestMouseSelectionDoesNotBlockPrefixChords(t *testing.T) {
	s, c := newHelpServer(80, 24)
	startMouseSelection(t, s, c, 0, 0)

	s.handleInput(c, []byte("\x01"))
	s.handleInput(c, []byte("z"))

	if c.selection != nil {
		t.Fatal("prefix chord did not clear the selection")
	}
	if !s.activeWindow().zoomed {
		t.Fatal("prefix chord was swallowed by the mouse selection")
	}
}

func TestSelectionDroppedWhenLayoutChanges(t *testing.T) {
	s, c := newHelpServer(80, 24)
	startMouseSelection(t, s, c, 0, 0)

	// Zoom is applied without a client message here, so the selection can only
	// be dropped by the compositor's staleness check.
	s.toggleZoom()

	if c.selection != nil {
		t.Fatal("selection survived a layout change")
	}
}

func TestSelectActionsSwallowedWhileSettingsOpen(t *testing.T) {
	s, c := newHelpServer(80, 24)
	c.settingsOpen = true
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: 1, Y: 1}})
	if c.selection != nil || c.copyMode != nil {
		t.Fatal("selection start leaked through the settings modal")
	}
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 2, Y: 2}})
	if c.selection != nil || c.copyMode != nil {
		t.Fatal("selection update leaked through the settings modal")
	}
}

func TestCopyKeyConsumesPendingPrefix(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "clip me")
	startMouseSelection(t, s, c, 0, 0)

	// Arm the prefix, then start a drag: the prefix is still pending.
	s.handleInput(c, []byte("\x01"))
	if !c.prefixArmed {
		t.Fatal("prefix did not arm")
	}
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: 0, Y: 0}})
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 6, Y: 0}})

	s.handleInput(c, []byte("\x19")) // Ctrl-Y copy key
	if _, ok := queuedClipboard(c); !ok {
		t.Fatal("copy key did not copy")
	}
	if c.prefixArmed || c.prefixBuf != nil {
		t.Fatal("copy key left the prefix armed for the next keystroke")
	}
}

func TestSelectionCaretHiddenOnlyForTheFrozenPane(t *testing.T) {
	s, c := newHelpServer(80, 24)
	sel := startMouseSelection(t, s, c, 0, 0)
	if c.frame == nil || c.frame.CursorVisible {
		t.Fatal("frozen selection view should hide the caret")
	}
	// A selection whose rect no longer matches its pane is not drawn, so the
	// pane stays live and keeps its caret. Compose is exercised directly: the
	// broadcast path would drop the stale selection before rendering.
	sel.rect = layout.Rect{X: 0, Y: 0, W: 1, H: 1}
	c.comp.selection = sel
	frame := c.comp.compose(s.sess, s.cols, s.rows, true)
	if !frame.CursorVisible {
		t.Fatal("stale selection hid the caret of a live pane")
	}
}
