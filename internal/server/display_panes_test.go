package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hinshun/vt10x"

	"github.com/demongiggle/cogito/internal/layout"
	"github.com/demongiggle/cogito/internal/protocol"
)

func newManyPaneServer(t *testing.T, count int) (*Server, *clientConn) {
	t.Helper()
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	for id := 3; id <= count; id++ {
		if !w.tree.Split(1, id, layout.Vertical) {
			t.Fatalf("could not add pane %d", id)
		}
		s.sess.panes[id] = &pane{id: id, vt: vt10x.New(vt10x.WithSize(80, 23))}
	}
	w.activePane = 1
	s.sess.nextPaneID = count + 1
	if w.tree.Len() != count {
		t.Fatalf("pane count = %d, want %d", w.tree.Len(), count)
	}
	return s, c
}

func activePaneNumber(f protocol.Frame) int {
	for _, p := range f.Panes {
		if p.Active && !p.Zoomed {
			return p.Number
		}
	}
	return -1
}

func TestPrefixQShowsStyledPaneNumbers(t *testing.T) {
	if !isExactBinding("q") {
		t.Fatal("prefix q is not registered")
	}
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "q")
	if !c.displayPanesOpen {
		t.Fatal("prefix q did not show pane numbers")
	}
	if c.frame == nil {
		t.Fatal("prefix q did not render a frame")
	}
	f := *c.frame
	if !f.Full {
		t.Fatal("pane-number overlay should force a full repaint")
	}
	text := frameText(f)
	for _, want := range []string{"000", "0 0", "111", "panes 0-1", "number selects"} {
		if !strings.Contains(text, want) {
			t.Errorf("pane-number frame missing %q:\n%s", want, text)
		}
	}
	if len(f.Panes) != 2 || f.Panes[0].Number != 0 || f.Panes[1].Number != 1 {
		t.Fatalf("pane metadata = %+v, want numbered 0 and 1", f.Panes)
	}
	if f.CursorVisible {
		t.Error("pane-number overlay should hide the pane cursor")
	}

	activeStyled, inactiveStyled := false, false
	for _, cell := range f.Cells {
		if cell.FG == colPaneNumberActiveFG && cell.BG == colPaneNumberActiveBG && cell.Ch == '0' {
			activeStyled = true
		}
		if cell.FG == colPaneNumberFG && cell.BG == colPaneNumberBG && cell.Ch == '1' {
			inactiveStyled = true
		}
	}
	if !activeStyled || !inactiveStyled {
		t.Errorf("missing pane-number styling: active=%v inactive=%v", activeStyled, inactiveStyled)
	}
}

func TestDisplayPanesFitsSmallTerminal(t *testing.T) {
	comp := &compositor{displayPanes: true}
	f := comp.compose(newTestSession(), 40, 10, true)
	text := frameText(f)
	for _, want := range []string{"000", "111", "0-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("small pane-number frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible {
		t.Error("small pane-number overlay should hide the pane cursor")
	}

	tooSmall := (&compositor{displayPanes: true}).compose(newTestSession(), 20, 5, true)
	if strings.Contains(frameText(tooSmall), "000") {
		t.Error("pane numbers should not cover the terminal-too-small notice")
	}
}

func TestDisplayPanesNumberSelectsPane(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	w.activePane = 2 // number 1 is active before the jump
	fireBinding(s, c, "q")

	s.handleInput(c, []byte("0"))
	if c.displayPanesOpen {
		t.Fatal("number did not dismiss the overlay")
	}
	if w.activePane != 1 {
		t.Fatalf("active pane = %d, want pane number 0 (ID 1)", w.activePane)
	}
	if c.frame == nil || activePaneNumber(*c.frame) != 0 {
		t.Fatalf("frame after selection = %+v, want active pane number 0", c.frame)
	}
	if text := frameText(*c.frame); strings.Contains(text, "panes 0-1") {
		t.Errorf("overlay hint survived selection:\n%s", text)
	}
}

func TestDisplayPanesCancelSwallowsInput(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	w.activePane = 2
	f, err := os.CreateTemp(t.TempDir(), "pane-input")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f.Close()
	s.sess.activePane().ptmx = f

	fireBinding(s, c, "q")
	s.handleInput(c, []byte("echo should-not-run\n"))
	if c.displayPanesOpen {
		t.Fatal("ordinary typing did not cancel the overlay")
	}
	if w.activePane != 2 {
		t.Fatal("cancel changed the active pane")
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read pane input: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("cancelled key reached pane: %q", raw)
	}

	// Mouse/action messages dismiss the overlay without reaching panes.
	fireBinding(s, c, "q")
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: 0, Y: 0}})
	if c.displayPanesOpen || w.activePane != 2 {
		t.Fatal("mouse focus leaked through the pane-number overlay")
	}
}

func TestDisplayPanesArePerClientAndExpire(t *testing.T) {
	s, c1 := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}

	fireBinding(s, c1, "q")
	if c1.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both client frames")
	}
	if !strings.Contains(frameText(*c1.frame), "panes 0-1") {
		t.Error("requesting client is missing pane numbers")
	}
	if strings.Contains(frameText(*c2.frame), "panes 0-1") {
		t.Error("pane numbers leaked to another attached client")
	}

	c1.displayPanesDeadline = time.Now().Add(-time.Millisecond)
	s.expireDisplayPanes()
	if c1.displayPanesOpen {
		t.Fatal("expired overlay stayed open")
	}
	if strings.Contains(frameText(*c1.frame), "panes 0-1") {
		t.Error("expired overlay is still rendered")
	}
}

func TestDisplayPanesSupportsTwoDigitNumbers(t *testing.T) {
	t.Run("second digit", func(t *testing.T) {
		s, c := newManyPaneServer(t, 11)
		w := s.activeWindow()
		fireBinding(s, c, "q")

		s.handleInput(c, []byte("1"))
		if !c.displayPanesOpen || c.displayPanesInput != "1" {
			t.Fatalf("ambiguous first digit = open:%v input:%q, want true/\"1\"", c.displayPanesOpen, c.displayPanesInput)
		}
		s.handleInput(c, []byte("0"))
		if c.displayPanesOpen || w.activePane != 11 {
			t.Fatalf("pane 10 selection = open:%v active:%d, want false/11", c.displayPanesOpen, w.activePane)
		}
	})

	t.Run("Enter commits prefix", func(t *testing.T) {
		s, c := newManyPaneServer(t, 11)
		w := s.activeWindow()
		fireBinding(s, c, "q")
		s.handleInput(c, []byte("1\r"))
		if c.displayPanesOpen || w.activePane != 2 {
			t.Fatalf("pane 1 selection = open:%v active:%d, want false/2", c.displayPanesOpen, w.activePane)
		}
	})

	t.Run("timeout commits prefix", func(t *testing.T) {
		s, c := newManyPaneServer(t, 11)
		w := s.activeWindow()
		fireBinding(s, c, "q")
		s.handleInput(c, []byte("1"))
		c.displayPanesDeadline = time.Now().Add(-time.Millisecond)
		s.expireDisplayPanes()
		if c.displayPanesOpen || w.activePane != 2 {
			t.Fatalf("pane 1 timeout = open:%v active:%d, want false/2", c.displayPanesOpen, w.activePane)
		}
	})

	t.Run("invalid number", func(t *testing.T) {
		s, c := newHelpServer(80, 24)
		fireBinding(s, c, "q")
		s.handleInput(c, []byte("9"))
		if c.displayPanesOpen {
			t.Fatal("invalid number left the overlay open")
		}
		foundNotice := false
		for _, msg := range c.queue {
			if _, ok := msg.(protocol.Notice); ok {
				foundNotice = true
			}
		}
		if !foundNotice {
			t.Error("invalid number did not ring the bell")
		}
	})
}

func TestDisplayPanesFromZoomShowsSplitAndUnzooms(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	w.zoomed = true
	s.relayout()

	fireBinding(s, c, "q")
	if c.frame == nil {
		t.Fatal("zoom display-panes did not render a frame")
	}
	f := *c.frame
	if len(f.Panes) != 2 {
		t.Fatalf("display-panes frame has %d panes, want the two split panes", len(f.Panes))
	}
	for _, p := range f.Panes {
		if p.Zoomed {
			t.Fatal("floating zoom overlay is still present while numbering panes")
		}
	}
	if f.CursorVisible {
		t.Error("pane-number overlay should hide the zoomed caret")
	}
	if !strings.Contains(frameText(f), "panes 0-1") {
		t.Error("zoom display-panes frame is missing the number hint")
	}

	s.handleInput(c, []byte("1"))
	if w.zoomed || w.activePane != 2 {
		t.Fatalf("jump from zoom = zoomed:%v active:%d, want false/2", w.zoomed, w.activePane)
	}
}

func TestPaneNumberPrefixHelpers(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		count  int
		valid  bool
		longer bool
	}{
		{"0", 2, true, false},
		{"1", 2, true, false},
		{"2", 2, false, false},
		{"1", 11, true, true},
		{"10", 11, true, false},
		{"11", 11, false, false},
		{"01", 11, false, false},
	} {
		if got := paneNumberHasPrefix(tc.prefix, tc.count); got != tc.valid {
			t.Errorf("paneNumberHasPrefix(%q, %d) = %v, want %v", tc.prefix, tc.count, got, tc.valid)
		}
		if got := paneNumberHasLongerMatch(tc.prefix, tc.count); got != tc.longer {
			t.Errorf("paneNumberHasLongerMatch(%q, %d) = %v, want %v", tc.prefix, tc.count, got, tc.longer)
		}
	}
}
