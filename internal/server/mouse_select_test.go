package server

import (
	"strings"
	"testing"

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

func TestSelectStartOpensCopyModeWithCharSelection(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	s.handleSelectStart(c, 0, 0)
	st := c.copyMode
	if st == nil || !st.open {
		t.Fatal("ActionSelectStart did not open copy mode")
	}
	if !st.mouseOrigin {
		t.Fatal("selection opened from a mouse gesture must set mouseOrigin")
	}
	if st.selKind != selectionChar {
		t.Fatalf("selKind = %v, want selectionChar", st.selKind)
	}
	if st.anchor != (copyPoint{line: 0, col: 0}) {
		t.Fatalf("anchor = %+v, want {0 0}", st.anchor)
	}

	s.handleSelectUpdate(c, 4, 0)
	if got := st.cursor; got != (copyPoint{line: 0, col: 4}) {
		t.Fatalf("cursor = %+v, want {0 4}", got)
	}
	if got := st.selectedText(); got != "hello" {
		t.Fatalf("selected text = %q, want %q", got, "hello")
	}
}

func TestSelectUpdateWithoutStartIsIgnored(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.handleSelectUpdate(c, 3, 3)
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

func TestConfiguredCopyKeyCopiesAndCloses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		bytes string
		idx   int
	}{
		{"default ctrl-shift-c", "Ctrl-Shift-C", "\x1b[27;6;99~", 0},
		{"ctrl-shift-x", "Ctrl-Shift-X", "\x1b[27;6;120~", 1},
		{"ctrl-y", "Ctrl-Y", "\x19", 2},
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
			s.handleSelectStart(c, 0, 0)
			s.handleSelectUpdate(c, 6, 0)

			s.handleCopyModeInput(c, []byte(tc.bytes))

			if c.copyMode != nil {
				t.Fatal("copy key did not close copy mode")
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

func TestCopyKeyFallsBackForUnsetSetting(t *testing.T) {
	st := defaultSettings()
	st.values[settingCopyKey] = 999 // out of range: falls back to default
	if got := copyKeySequence(&st); got != "\x1b[27;6;99~" {
		t.Fatalf("out-of-range copy key = %q, want default Ctrl-Shift-C sequence", got)
	}
	if got := copyKeySequence(nil); got != "\x1b[27;6;99~" {
		t.Fatalf("nil settings copy key = %q, want default Ctrl-Shift-C sequence", got)
	}
}

func TestWheelExtendsMouseSelectionIntoScrollback(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(40, "row %02d"))
	st.mouseOrigin = true
	st.selKind = selectionChar
	st.anchor = copyPoint{line: 30, col: 0}
	st.cursor = copyPoint{line: 30, col: 0}
	st.ensureVisible()
	topBefore := st.top
	if topBefore == 0 {
		t.Fatal("fixture expected a scrolled-down viewport")
	}
	s.handleWheel(c, -1) // wheel up one notch
	if st.top != max(topBefore-wheelStepLines, 0) {
		t.Fatalf("top = %d, want %d", st.top, max(topBefore-wheelStepLines, 0))
	}
	if st.cursor.line != 30-(topBefore-st.top) {
		t.Fatalf("cursor.line = %d, want it to ride the view upward into scrollback", st.cursor.line)
	}
	if a, b := st.selectionBounds(); b.line < a.line {
		t.Fatalf("inverted bounds after wheel: %+v %+v", a, b)
	}
}

func TestMouseSelectActionsSwallowedWhileSettingsOpen(t *testing.T) {
	s, c := newHelpServer(80, 24)
	c.settingsOpen = true
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: 1, Y: 1}})
	if c.copyMode != nil {
		t.Fatal("selection start leaked through the settings modal")
	}
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 2, Y: 2}})
	if c.copyMode != nil {
		t.Fatal("selection update leaked through the settings modal")
	}
}

func TestMouseSelectionKeepsNormalStatusBar(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	s.handleSelectStart(c, 0, 0)
	s.handleSelectUpdate(c, 4, 0)
	if c.frame == nil {
		t.Fatal("no frame after selection update")
	}
	text := frameText(*c.frame)
	if strings.Contains(text, "[copy mode]") {
		t.Fatal("mouse-origin selection rendered the copy-mode hint bar")
	}
	if !strings.Contains(text, "0:zsh") {
		t.Fatal("mouse-origin selection did not keep the normal status bar")
	}
	if len(c.frame.Tabs) == 0 {
		t.Fatal("mouse-origin selection did not keep status bar tabs hit-testable")
	}

	// A keyboard-opened copy mode keeps the copy hint instead.
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}
	c2.copyMode = &copyModeState{
		open:   true,
		paneID: 1,
		lines:  []copyLine{lineOf("hello world")},
		cols:   80,
		rows:   24,
	}
	c2.forceFull = true
	s.broadcast()
	if c2.frame == nil {
		t.Fatal("no frame for keyboard copy mode")
	}
	text = frameText(*c2.frame)
	if !strings.Contains(text, "[copy mode]") {
		t.Fatal("keyboard copy mode lost the hint bar")
	}
}

func TestMouseSelectionCopyFlowRestoresLiveMode(t *testing.T) {
	s, c := newHelpServer(80, 24)
	writePaneText(t, s, 1, "hello world")

	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: 0, Y: 0}})
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectUpdate{X: 4, Y: 0}})
	s.handleCopyModeInput(c, []byte("\x1b[27;6;99~"))

	if c.copyMode != nil {
		t.Fatal("copy did not close copy mode")
	}
	clip, ok := queuedClipboard(c)
	if !ok {
		t.Fatal("no Clipboard message after copy")
	}
	if clip.Text != "hello" {
		t.Fatalf("clipboard = %q, want %q", clip.Text, "hello")
	}
}

func TestSelectStartWhileKeyboardCopyModeOpenReanchors(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(10, "row %02d"))
	st.cursor = copyPoint{line: 9, col: 4}
	st.anchor = st.cursor
	s.handleSelectStart(c, 2, 1)
	if !st.mouseOrigin {
		t.Fatal("mouse origin flag not set on re-anchor")
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
