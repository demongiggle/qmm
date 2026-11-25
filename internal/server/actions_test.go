package server

import (
	"testing"

	"github.com/demongiggle/cogito/internal/protocol"
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
