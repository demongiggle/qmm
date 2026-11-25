package server

import (
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/protocol"
)

func newHelpClient() *clientConn {
	return &clientConn{
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

func newHelpServer(cols, rows int) (*Server, *clientConn) {
	c := newHelpClient()
	s := &Server{
		sess:    newTestSession(),
		cols:    cols,
		rows:    rows,
		clients: map[*clientConn]struct{}{c: {}},
	}
	return s, c
}

func TestPrefixQuestionOpensStyledHelp(t *testing.T) {
	if !isExactBinding("?") {
		t.Fatal("prefix ? is not registered")
	}
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "?")
	if !c.helpOpen {
		t.Fatal("prefix ? did not open help")
	}
	if c.frame == nil {
		t.Fatal("help binding did not render a frame")
	}
	f := *c.frame
	text := frameText(f)
	for _, want := range []string{
		"QMM HELP", "PREFIX: Ctrl-a",
		"PANES", "WINDOWS", "LAYOUT", "SESSION", "MOUSE",
		"C-a ?", "Show this help", "Esc/q close",
		"C-a z", "Floating zoom",
		"Double-click", "Zoom / restore",
		"C-a &", "Kill session (y/n)",
		"C-a |", "Split left/right", "C-a -", "Split top/bottom",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible {
		t.Error("help overlay should hide the pane cursor")
	}
	if len(f.Panes) != 0 || len(f.Splitters) != 0 {
		t.Error("help overlay should hide pane hit targets")
	}

	keyHighlighted, headerHighlighted := false, false
	for _, cell := range f.Cells {
		if cell.FG == colHelpKeyFG && cell.BG == colHelpKeyBG && cell.Style&protocol.StyleBold != 0 {
			keyHighlighted = true
		}
		if cell.FG == colHelpHeader && cell.Style&protocol.StyleBold != 0 {
			headerHighlighted = true
		}
	}
	if !keyHighlighted || !headerHighlighted {
		t.Errorf("missing help highlights: key=%v header=%v", keyHighlighted, headerHighlighted)
	}
}

func TestHelpOverlayIsPerClient(t *testing.T) {
	s, c1 := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}

	c1.helpOpen = true
	c1.forceFull = true
	s.broadcast()

	if c1.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both client frames")
	}
	if !strings.Contains(frameText(*c1.frame), "QMM HELP") {
		t.Error("requesting client is missing the help overlay")
	}
	if strings.Contains(frameText(*c2.frame), "QMM HELP") {
		t.Error("help overlay leaked to another attached client")
	}
}

func TestHelpInputScrollsAndDismisses(t *testing.T) {
	s, c := newHelpServer(40, 10)
	page, maxTop := helpScrollMetrics(s.cols, s.rows)
	if maxTop == 0 {
		t.Fatal("expected a scrollable help menu at 40x10")
	}
	c.helpOpen = true
	c.forceFull = true
	s.broadcast()

	s.handleHelpInput(c, []byte("\x1b[B"))
	if !c.helpOpen || c.helpTop != 1 {
		t.Fatalf("down arrow = open:%v top:%d, want open:true top:1", c.helpOpen, c.helpTop)
	}
	s.handleHelpInput(c, []byte("\x1bOB")) // SS3 down-arrow encoding
	if !c.helpOpen || c.helpTop != 2 {
		t.Fatalf("SS3 down arrow = open:%v top:%d, want open:true top:2", c.helpOpen, c.helpTop)
	}
	s.handleHelpInput(c, []byte("\x1b[6~"))
	if want := min(2+page, maxTop); c.helpTop != want {
		t.Fatalf("page down top = %d, want %d", c.helpTop, want)
	}
	s.handleHelpInput(c, []byte("q"))
	if c.helpOpen {
		t.Fatal("q did not close help")
	}
}

func TestHelpOverlayClampsScrollOnSmallTerminal(t *testing.T) {
	comp := &compositor{helpOpen: true, helpTop: 999}
	f := comp.compose(newTestSession(), 40, 10, true)
	_, want := helpScrollMetrics(40, 10)
	if comp.helpTop != want {
		t.Fatalf("helpTop = %d, want clamped to %d", comp.helpTop, want)
	}
	text := frameText(f)
	if !strings.Contains(text, "q/Esc close") || !strings.Contains(text, "↑/↓") {
		t.Fatalf("small help frame missing scroll hints:\n%s", text)
	}
}

func TestMergeFramesKeepsCellsFromCoalescedFrames(t *testing.T) {
	old := protocol.Frame{
		Cols: 2, Rows: 1,
		Cells: []protocol.Cell{{Row: 0, Col: 0, Ch: 'A'}, {Row: 0, Col: 1, Ch: ' '}},
	}
	next := protocol.Frame{
		Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{{Row: 0, Col: 1, Ch: 'B'}},
		CursorVisible: true,
	}
	merged := mergeFrames(old, next)
	if len(merged.Cells) != 2 || merged.Cells[0].Ch != 'A' || merged.Cells[1].Ch != 'B' {
		t.Fatalf("merged cells = %+v, want A and B", merged.Cells)
	}
	if !merged.CursorVisible {
		t.Error("merged frame lost newer cursor metadata")
	}
}
