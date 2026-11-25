package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestSplitPrefixBindings(t *testing.T) {
	// "|" and "-" replace the tmux-style "%" and "\"" split keys.
	for _, seq := range []string{"|", "-"} {
		if !isExactBinding(seq) {
			t.Errorf("prefix %q is not bound", seq)
		}
	}
	for _, legacy := range []string{"%", "\""} {
		if isExactBinding(legacy) {
			t.Errorf("legacy split key %q is still bound", legacy)
		}
	}
}

func TestTogglePaneZoomActionFocusesAndRestores(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	_, inner := zoomRects(80, 23)

	// A double-click on the inactive pane focuses and floats it.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 2}})
	if !w.zoomed || w.activePane != 2 {
		t.Fatalf("double-click zoom = zoomed:%v active:%d, want true/2", w.zoomed, w.activePane)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("zoomed pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}

	// Double-clicking a different exposed pane moves the overlay to it.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 1}})
	if !w.zoomed || w.activePane != 1 {
		t.Fatalf("double-click switch = zoomed:%v active:%d, want true/1", w.zoomed, w.activePane)
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("switched zoom pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != 39 || rows != 23 {
		t.Fatalf("old zoom pane size = %dx%d, want normal 39x23", cols, rows)
	}

	// Double-clicking the floating pane restores the split.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 1}})
	if w.zoomed {
		t.Fatal("double-click on floating pane did not restore the split")
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != 40 || rows != 23 {
		t.Fatalf("restored pane size = %dx%d, want normal 40x23", cols, rows)
	}
}

func TestCyclePaneResizesZoomedOverlay(t *testing.T) {
	s, _ := newHelpServer(80, 24)
	w := s.activeWindow()
	w.zoomed = true
	s.relayout()
	_, inner := zoomRects(80, 23)

	s.cyclePane()
	if w.activePane != 2 {
		t.Fatalf("cycle pane active = %d, want 2", w.activePane)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("new zoomed pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != 40 || rows != 23 {
		t.Fatalf("old zoomed pane size = %dx%d, want normal 40x23", cols, rows)
	}
}

func repeatWindowFrameSplitterX(t *testing.T, c *clientConn) int {
	t.Helper()
	if c.frame == nil || len(c.frame.Splitters) != 1 {
		t.Fatalf("frame splitters = %+v, want exactly one", c.frame)
	}
	return c.frame.Splitters[0].X
}

func TestPrefixStateMachineRepeatsCtrlArrowResize(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.broadcast()
	startX := repeatWindowFrameSplitterX(t, c)

	// One prefix, then three Ctrl-Right presses. Both key-event layouts are
	// regression-covered: several sequences in one Input and one per Input.
	s.handleInput(c, []byte("\x01"+strings.Repeat("\x1b[1;5C", 2)))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+4 {
		t.Fatalf("two Ctrl-Right presses = X:%d, want X:%d", got, startX+4)
	}
	if !c.repeatArmed {
		t.Fatal("repeatable resize binding did not open a repeat window")
	}

	s.handleInput(c, []byte("\x1b[1;5C"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+6 {
		t.Fatalf("third Ctrl-Right press = X:%d, want X:%d", got, startX+6)
	}

	// A different direction is also repeatable, including the rxvt encoding.
	s.handleInput(c, []byte("\x1b[5D"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+4 {
		t.Fatalf("rxvt Ctrl-Left repeat = X:%d, want X:%d", got, startX+4)
	}

	// A fresh prefix chord still works while the repeat window is active.
	s.handleInput(c, []byte("\x01\x1b[1;5D"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
		t.Fatalf("prefix Ctrl-Left after repeat = X:%d, want X:%d", got, startX+2)
	}
}

func TestPrefixStateMachineEndsCtrlArrowRepeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endRepeat  func(*clientConn)
		otherInput []byte
	}{
		{
			name:       "other key",
			otherInput: []byte("x"),
		},
		{
			name:       "partial escape sequence",
			otherInput: []byte("\x1b[1;5"),
		},
		{
			name: "timeout",
			endRepeat: func(c *clientConn) {
				c.repeatAt = time.Now().Add(-repeatTimeout - time.Millisecond)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			f, err := os.CreateTemp(t.TempDir(), "pane-input")
			if err != nil {
				t.Fatalf("temp pane input: %v", err)
			}
			defer f.Close()
			s.sess.activePane().ptmx = f
			s.broadcast()
			startX := repeatWindowFrameSplitterX(t, c)

			s.handleInput(c, []byte("\x01\x1b[1;5C"))
			if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
				t.Fatalf("first resize = X:%d, want X:%d", got, startX+2)
			}
			if tc.otherInput != nil {
				s.handleInput(c, tc.otherInput)
			}
			if tc.endRepeat != nil {
				tc.endRepeat(c)
			}

			s.handleInput(c, []byte("\x1b[1;5C"))
			if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
				t.Fatalf("Ctrl-Right after repeat ended = X:%d, want X:%d", got, startX+2)
			}
			if c.repeatArmed {
				t.Fatal("repeat window stayed active")
			}
			wantRaw := "\x1b[1;5C"
			if tc.otherInput != nil {
				wantRaw = string(tc.otherInput) + wantRaw
			}
			raw, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatalf("read pane input: %v", err)
			}
			if string(raw) != wantRaw {
				t.Fatalf("pane input = %q, want %q", raw, wantRaw)
			}
		})
	}
}
