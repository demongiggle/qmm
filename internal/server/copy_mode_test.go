package server

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/protocol"
)

func lineOf(text string) copyLine {
	cells := make([]copyCell, 0, len(text))
	for _, ch := range text {
		cells = append(cells, copyCell{Ch: ch, FG: protocol.ColorDefault, BG: protocol.ColorDefault})
	}
	return copyLine{cells: cells}
}

func linesOf(n int, format string) []copyLine {
	lines := make([]copyLine, n)
	for i := 0; i < n; i++ {
		lines[i] = lineOf(fmt.Sprintf(format, i))
	}
	return lines
}

func newCopyModeFixture(cols, rows int, lines []copyLine) (*Server, *clientConn, *copyModeState) {
	s, c := newHelpServer(cols, rows)
	st := &copyModeState{
		open:   true,
		paneID: 1,
		lines:  lines,
		cols:   cols,
		rows:   rows,
	}
	st.cursor.line = len(lines) - 1
	st.top = st.maxTop()
	st.clampCursor()
	c.copyMode = st
	return s, c, st
}

func copyKeys(s *Server, c *clientConn, keys ...string) {
	for _, key := range keys {
		s.handleCopyModeInput(c, []byte(key))
	}
}

func queuedClipboard(c *clientConn) (protocol.Clipboard, bool) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	for _, msg := range c.queue {
		if clip, ok := msg.(protocol.Clipboard); ok {
			return clip, true
		}
	}
	return protocol.Clipboard{}, false
}

func TestCopyModeVimMotions(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(30, "line %02d"))
	st.gotoLine(0)

	copyKeys(s, c, "3", "j")
	if st.cursor.line != 3 {
		t.Fatalf("3j line = %d, want 3", st.cursor.line)
	}
	copyKeys(s, c, "9", "9", "j") // a large count clamps to the last line
	if st.cursor.line != 29 {
		t.Fatalf("10j line = %d, want 29", st.cursor.line)
	}
	copyKeys(s, c, "$")
	if want := len("line 29") - 1; st.cursor.col != want {
		t.Fatalf("$ col = %d, want %d", st.cursor.col, want)
	}
	copyKeys(s, c, "0")
	if st.cursor.col != 0 {
		t.Fatalf("0 col = %d, want 0", st.cursor.col)
	}
	copyKeys(s, c, "w")
	if st.cursor.col != 5 {
		t.Fatalf("w col = %d, want 5 (start of number)", st.cursor.col)
	}
	copyKeys(s, c, "b")
	if st.cursor.col != 0 {
		t.Fatalf("b col = %d, want 0", st.cursor.col)
	}
	copyKeys(s, c, "g", "g")
	if st.cursor.line != 0 {
		t.Fatalf("gg line = %d, want 0", st.cursor.line)
	}
	copyKeys(s, c, "1", "5", "G")
	if st.cursor.line != 14 {
		t.Fatalf("15G line = %d, want 14", st.cursor.line)
	}
	copyKeys(s, c, "G")
	if st.cursor.line != 29 {
		t.Fatalf("G line = %d, want 29", st.cursor.line)
	}
	// Cursor motions keep the viewport in sync on a buffer taller than the
	// screen.
	if st.top != st.maxTop() {
		t.Fatalf("top = %d, want %d after G", st.top, st.maxTop())
	}
}

func TestCopyModePageScrollAndExit(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 10, linesOf(100, "row %03d"))
	st.gotoLine(0)
	copyKeys(s, c, "\x04") // Ctrl-d: half a page down
	if st.top == 0 {
		t.Fatal("Ctrl-d did not scroll the viewport")
	}
	copyKeys(s, c, "\x15") // Ctrl-u: back up
	if st.top != 0 {
		t.Fatalf("Ctrl-u top = %d, want 0", st.top)
	}
	copyKeys(s, c, "q")
	if c.copyMode != nil {
		t.Fatal("q did not close copy mode")
	}
}

func TestCopyModeCharwiseSelectionCopiesAndPastes(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, []copyLine{lineOf("alpha"), lineOf("bravo")})
	st.gotoLine(0)

	copyKeys(s, c, "v", "l", "j", "y")
	clip, ok := queuedClipboard(c)
	if !ok {
		t.Fatal("y did not emit a Clipboard message")
	}
	if clip.Text != "alpha\nbr" {
		t.Fatalf("clipboard = %q, want %q", clip.Text, "alpha\nbr")
	}
	if s.pasteBuffer != clip.Text {
		t.Fatalf("paste buffer = %q, want %q", s.pasteBuffer, clip.Text)
	}
	if c.copyMode != nil {
		t.Fatal("copy should close copy mode")
	}

	f, err := os.CreateTemp(t.TempDir(), "ptmx")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s.sess.panes[1].ptmx = f
	s.pasteClipboard(c)
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _ := f.Read(buf)
	if got := string(buf[:n]); got != "alpha\nbr" {
		t.Fatalf("pane received %q, want %q", got, "alpha\nbr")
	}
}

func TestCopyModeLinewiseAndRectSelection(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, []copyLine{lineOf("alpha"), lineOf("bravo"), lineOf("charlie")})
	st.gotoLine(0)
	copyKeys(s, c, "V", "j", "y")
	clip, ok := queuedClipboard(c)
	if !ok || clip.Text != "alpha\nbravo" {
		t.Fatalf("linewise clipboard = %q, ok=%v", clip.Text, ok)
	}

	s2, c2, st2 := newCopyModeFixture(80, 24, []copyLine{lineOf("alpha"), lineOf("bravo")})
	st2.gotoLine(0)
	copyKeys(s2, c2, "\x16", "2", "l", "j", "y")
	clip2, ok := queuedClipboard(c2)
	if !ok || clip2.Text != "alp\nbra" {
		t.Fatalf("rect clipboard = %q, ok=%v", clip2.Text, ok)
	}
}

func TestCopyModeSearch(t *testing.T) {
	lines := linesOf(20, "ordinary %02d")
	lines[5] = lineOf("needle here")
	lines[12] = lineOf("another needle")
	s, c, st := newCopyModeFixture(80, 24, lines)
	st.gotoLine(0)
	copyKeys(s, c, "/", "n", "e", "e", "d", "l", "e", "\r")
	if st.cursor.line != 5 {
		t.Fatalf("search line = %d, want 5", st.cursor.line)
	}
	copyKeys(s, c, "n")
	if st.cursor.line != 12 {
		t.Fatalf("n line = %d, want 12", st.cursor.line)
	}
	copyKeys(s, c, "N")
	if st.cursor.line != 5 {
		t.Fatalf("N line = %d, want 5", st.cursor.line)
	}
}

func TestCopyModeOverlayIsPerClientAndHidesCursor(t *testing.T) {
	s, c, _ := newCopyModeFixture(80, 24, []copyLine{lineOf("hello copy mode")})
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}
	s.broadcast()
	if c.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both frames")
	}
	f := *c.frame
	if f.CursorVisible {
		t.Error("copy mode should hide the native pane cursor")
	}
	if len(f.Panes) != 0 || len(f.Splitters) != 0 || len(f.Tabs) != 0 {
		t.Error("copy mode should hide pane hit targets and status tabs")
	}
	text := frameText(f)
	if !strings.Contains(text, "hello copy mode") || !strings.Contains(text, "[copy mode]") {
		t.Fatalf("copy mode frame missing content/hint:\n%s", text)
	}
	if strings.Contains(frameText(*c2.frame), "[copy mode]") {
		t.Error("copy mode overlay leaked to another client")
	}
}

func TestPrefixBracketBindingsAndWheel(t *testing.T) {
	if !isExactBinding("[") || !isExactBinding("]") {
		t.Fatal("prefix [ and ] are not registered")
	}
	s, c := newHelpServer(80, 24)
	p := s.sess.activePane()
	_, _ = p.vt.Write([]byte("first\r\nsecond"))
	s.handleWheel(c, -1)
	if c.copyMode == nil || !c.copyMode.open {
		t.Fatal("wheel up did not open copy mode")
	}
	if got := c.copyMode.lines[0].text(); got != "first" {
		t.Fatalf("first copy line = %q, want first", got)
	}
	s.handleWheel(c, -1)
	if c.copyMode == nil {
		t.Fatal("wheel up closed copy mode")
	}
	s.handleWheel(c, 1)
	s.handleWheel(c, 1)
	if c.copyMode != nil {
		t.Fatal("wheel down at the bottom should close copy mode")
	}
}

func TestCopyModeGuardSwallowsPaneActions(t *testing.T) {
	s, c, _ := newCopyModeFixture(80, 24, []copyLine{lineOf("text")})
	before := s.sess.active
	if s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionNextWindow{}}) {
		t.Fatal("unexpected shutdown request")
	}
	if s.sess.active != before {
		t.Fatal("pane action changed the session while copy mode was open")
	}
	if !s.handleClientMessage(clientMsg{cli: c, msg: protocol.Input{Data: []byte("q")}}) && c.copyMode != nil {
		t.Fatal("copy-mode input was not handled")
	}
}
