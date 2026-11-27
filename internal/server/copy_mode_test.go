package server

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/demongiggle/qmm/internal/layout"
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
		rect:   s.paneRects()[1],
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

func TestCopyModeViewIsPerClientAndHidesCursor(t *testing.T) {
	s, c, _ := newCopyModeFixture(80, 24, []copyLine{lineOf("hello copy mode")})
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}
	s.broadcast()
	if c.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both frames")
	}
	f := *c.frame
	if f.CursorVisible {
		t.Error("copy mode should hide its pane's native cursor")
	}
	if len(f.Panes) != 2 || len(f.Splitters) != 1 || len(f.Tabs) == 0 {
		t.Error("copy mode should keep the pane layout, borders and status tabs")
	}
	text := frameText(f)
	if !strings.Contains(text, "hello copy mode") || !strings.Contains(text, "[copy mode]") {
		t.Fatalf("copy mode frame missing content/hint:\n%s", text)
	}
	if strings.Contains(frameText(*c2.frame), "[copy mode]") {
		t.Error("copy mode overlay leaked to another client")
	}
}

// rectText returns the characters inside r, one screen row per line.
func rectText(f protocol.Frame, r layout.Rect) string {
	var sb strings.Builder
	for y := r.Y; y < r.Bottom(); y++ {
		for x := r.X; x < r.Right(); x++ {
			sb.WriteRune(cellAt(f, x, y).Ch)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestCopyModeStaysInsideItsPane(t *testing.T) {
	s, c := newHelpServer(80, 24)
	rects := s.paneRects()
	if len(rects) != 2 {
		t.Fatalf("fixture has %d panes, want 2", len(rects))
	}
	_, _ = s.sess.panes[1].vt.Write([]byte("PANE-ONE"))
	_, _ = s.sess.panes[2].vt.Write([]byte("PANE-TWO"))
	s.openCopyMode(c, 0)
	if c.copyMode == nil || !c.copyMode.open {
		t.Fatal("prefix+[ did not open copy mode")
	}
	if got, want := c.copyMode.rect, rects[1]; got != want {
		t.Fatalf("copy rect = %+v, want the active pane %+v", got, want)
	}
	// Show the top of the snapshot, where the pane's text sits.
	copyKeys(s, c, "g", "g")

	f := *c.frame
	if len(f.Panes) != 2 {
		t.Fatalf("frame has %d pane hit targets, want 2", len(f.Panes))
	}
	// The other pane keeps running and stays visible.
	if got := rectText(f, rects[2]); !strings.Contains(got, "PANE-TWO") {
		t.Fatalf("copy mode hid the other pane:\n%s", got)
	}
	// The copy view covers only its own pane, status row included.
	paneText := rectText(f, rects[1])
	if !strings.Contains(paneText, "PANE-ONE") || !strings.Contains(paneText, "[copy mode]") {
		t.Fatalf("copy pane missing snapshot/hint:\n%s", paneText)
	}
	statusY := rects[1].Bottom() - 1
	if got := cellAt(f, rects[1].Right()-1, statusY).BG; got != colCopyStatusBG {
		t.Fatalf("copy status row background = %d, want %d", got, colCopyStatusBG)
	}
	if got := cellAt(f, rects[1].Right(), statusY).BG; got == colCopyStatusBG {
		t.Fatal("copy status row painted over the pane border")
	}
	if got := cellAt(f, rects[2].X, statusY).BG; got == colCopyStatusBG {
		t.Fatal("copy status row painted over the other pane")
	}
	if got := rectText(f, rects[2]); strings.Contains(got, "copy mode") {
		t.Fatalf("copy status leaked into the other pane:\n%s", got)
	}
	// The session status bar is still there, with its window tab.
	bar := rectText(f, layout.Rect{Y: 23, W: 80, H: 1})
	if !strings.Contains(bar, "0:zsh*") {
		t.Fatalf("copy mode hid the status bar:\n%s", bar)
	}
}

func TestCopyModeFollowsPaneRect(t *testing.T) {
	s, _, st := newCopyModeFixture(80, 24, linesOf(60, "line %02d"))
	if st.rect != s.paneRects()[1] {
		t.Fatalf("fixture rect = %+v, want the pane rect", st.rect)
	}

	// A resize moves the pane; the viewport follows instead of keeping the
	// old screen-sized rect.
	s.cols, s.rows = 60, 20
	s.relayout()
	s.broadcast()
	if want := s.paneRects()[1]; st.rect != want {
		t.Fatalf("rect after resize = %+v, want %+v", st.rect, want)
	}
	if st.top > st.maxTop() {
		t.Fatalf("top = %d is past maxTop %d after resize", st.top, st.maxTop())
	}

	// Zooming the window shows only the active pane; a copy view on a pane
	// that left the layout closes rather than drawing over the wrong cells.
	s2, c2, st2 := newCopyModeFixture(80, 24, linesOf(60, "line %02d"))
	st2.paneID = 2
	st2.rect = s2.paneRects()[2]
	s2.toggleZoom()
	if c2.copyMode != nil && c2.copyMode.open {
		t.Fatalf("copy mode on a hidden pane stayed open: %+v", c2.copyMode)
	}
}

func TestCopyModeMouseMapsWithinPane(t *testing.T) {
	s, c, st := newCopyModeFixture(80, 24, linesOf(60, "line %02d"))
	st.rect = layout.Rect{X: 10, Y: 5, W: 20, H: 11}
	st.top = 30
	st.cursor = copyPoint{line: 30}

	p := st.scrToBuffer(12, 7)
	if want := (copyPoint{line: 32, col: 2}); p != want {
		t.Fatalf("scrToBuffer(12,7) = %+v, want %+v", p, want)
	}
	// Points outside the pane clamp to its edges, so a drag can leave it.
	p = st.scrToBuffer(0, 0)
	if want := (copyPoint{line: 30, col: 0}); p != want {
		t.Fatalf("scrToBuffer(0,0) = %+v, want %+v", p, want)
	}
	p = st.scrToBuffer(79, 23)
	if p.line != 30+st.viewRows()-1 {
		t.Fatalf("scrToBuffer(79,23) line = %d, want the pane's last view row", p.line)
	}

	// A drag started inside the copy view anchors copy mode's selection.
	s.handleSelectStart(c, 12, 7)
	if c.selection != nil {
		t.Fatal("a drag inside copy mode must not open a mouse selection")
	}
	if !st.mouseDrag || st.anchor != (copyPoint{line: 32, col: 2}) {
		t.Fatalf("copy-mode anchor = %+v drag=%v, want the pressed cell", st.anchor, st.mouseDrag)
	}
}

func TestCopyModeClickOutsideReturnsToLayout(t *testing.T) {
	s, c, _ := newCopyModeFixture(80, 24, []copyLine{lineOf("hello copy mode")})
	rects := s.paneRects()
	other := rects[2]

	// Pressing another pane closes copy mode and focuses that pane.
	x, y := other.X+2, other.Y+1
	if s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: x, Y: y}}) {
		t.Fatal("unexpected shutdown request")
	}
	if c.copyMode != nil {
		t.Fatal("pressing another pane should leave copy mode")
	}
	if got := s.sess.activeWindow().activePane; got != 2 {
		t.Fatalf("active pane = %d, want 2", got)
	}

	// The drag that follows becomes an ordinary mouse selection.
	if s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionSelectStart{X: x, Y: y}}) {
		t.Fatal("unexpected shutdown request")
	}
	if c.selection == nil || c.selection.paneID != 2 {
		t.Fatalf("selection = %+v, want one on pane 2", c.selection)
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

func TestCopySelectionClampsOnUTF8Boundary(t *testing.T) {
	// 'ü' is two bytes and straddles the byte limit; slicing at the raw
	// limit would leave a lone continuation byte in the clipboard.
	tail := strings.Repeat("a", clipboardLimit-1) + "ü"
	want := strings.Repeat("a", clipboardLimit-1)
	if got := clampClipboardText(tail, clipboardLimit); got != want || !utf8.ValidString(got) {
		t.Fatalf("clamp = %d valid=%v, want %d-byte valid prefix", len(got), utf8.ValidString(got), len(want))
	}
	exact := strings.Repeat("b", clipboardLimit)
	if got := clampClipboardText(exact, clipboardLimit); got != exact {
		t.Fatal("a limit on a rune boundary should keep the whole text")
	}

	// The copy path stores and sends the same clamped text.
	s, c, st := newCopyModeFixture(80, 24, []copyLine{lineOf(tail)})
	st.gotoLine(0)
	s.copySelection(c)
	clip, ok := queuedClipboard(c)
	if !ok {
		t.Fatal("copy did not emit a Clipboard message")
	}
	if !utf8.ValidString(clip.Text) || clip.Text != want {
		t.Fatalf("clipboard len=%d valid=%v, want %d-byte valid prefix", len(clip.Text), utf8.ValidString(clip.Text), len(want))
	}
	if s.pasteBuffer != want {
		t.Fatalf("paste buffer len=%d, want %d", len(s.pasteBuffer), len(want))
	}
}
