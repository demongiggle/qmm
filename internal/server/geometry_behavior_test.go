package server

import (
	"slices"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
)

func TestNestedZoomUsesSmallestClientDimensionsAndRestoresSplit(t *testing.T) {
	s, owner := newHelpServer(100, 34)
	owner.cols, owner.rows = 100, 34
	peer := newHelpClient()
	peer.cols, peer.rows = 120, 40
	s.clients[peer] = struct{}{}
	w := s.activeWindow()
	if !w.tree.Split(2, 3, layout.Horizontal) {
		t.Fatal("could not create a nested split")
	}
	p := &pane{id: 3, vt: vt10x.New()}
	s.sess.panes[p.id] = p
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	p.ptmx = master
	s.relayout()
	writePaneText(t, s, p.id, "bottom marker")

	checkPaneSize := func(wantCols, wantRows int) {
		t.Helper()
		ptyRows, ptyCols, err := pty.Getsize(slave)
		if err != nil {
			t.Fatal(err)
		}
		vtCols, vtRows := p.vt.Size()
		if ptyCols != wantCols || ptyRows != wantRows || vtCols != wantCols || vtRows != wantRows {
			t.Fatalf("bottom pane: PTY %dx%d, VT %dx%d, want both %dx%d", ptyCols, ptyRows, vtCols, vtRows, wantCols, wantRows)
		}
	}
	checkPaneSize(49, 16)
	s.handleClientMessage(clientMsg{owner, protocol.ActionFocus{X: 60, Y: 20}})
	if got := behaviorActivePane(t, peer).ID; got != p.id {
		t.Fatalf("peer's focused pane = %d, want bottom pane %d", got, p.id)
	}
	sendBehaviorInput(s, owner, "\x01z")

	checkZoom := func(c *clientConn, cols, rows, innerW, innerH int) {
		t.Helper()
		f := behaviorFrame(t, c)
		if f.Cols != cols || f.Rows != rows || len(f.Splitters) != 0 {
			t.Fatalf("zoom frame = %dx%d with %d splitters, want %dx%d with none", f.Cols, f.Rows, len(f.Splitters), cols, rows)
		}
		want := protocol.PaneRect{ID: 3, Number: 2, X: 2, Y: 1, W: innerW + 2, H: innerH + 2, Active: true, Zoomed: true}
		if got := behaviorActivePane(t, c); got != want {
			t.Fatalf("zoom rect = %+v, want %+v", got, want)
		}
		if got := cellAt(f, 3, 2).Ch; got != 'b' {
			t.Fatalf("first zoom content cell = %q, want bottom pane marker", got)
		}
	}
	checkZoom(owner, 100, 34, 94, 29)
	checkPaneSize(94, 29)

	// The peer supplies the smaller width. The owner still supplies the
	// smaller height. Resize messages affect the whole shared layout.
	s.handleClientMessage(clientMsg{peer, protocol.Resize{Cols: 80, Rows: 36}})
	checkZoom(owner, 80, 34, 74, 29)
	checkZoom(peer, 80, 34, 74, 29)
	checkPaneSize(74, 29)

	sendBehaviorInput(s, peer, "\x01q")
	s.handleClientMessage(clientMsg{owner, protocol.Resize{Cols: 100, Rows: 26}})
	checkZoom(owner, 80, 26, 74, 21)
	checkPaneSize(74, 21)
	peek := behaviorFrame(t, peer)
	wantSplit := []protocol.PaneRect{
		{ID: 1, Number: 0, X: 0, Y: 0, W: 40, H: 25},
		{ID: 2, Number: 1, X: 41, Y: 0, W: 39, H: 12},
		{ID: 3, Number: 2, X: 41, Y: 13, W: 39, H: 12, Active: true},
	}
	if !slices.Equal(peek.Panes, wantSplit) || peek.CursorVisible || !strings.Contains(frameText(peek), "panes 0-2") {
		t.Fatalf("peer's pane picker after resize has panes %+v and cursor visible=%t", peek.Panes, peek.CursorVisible)
	}

	// Selecting a different pane through the picker removes the shared zoom
	// and restores the zoomed pane's actual PTY and VT to its split size.
	sendBehaviorInput(s, peer, "0")
	wantSplit[0].Active, wantSplit[2].Active = true, false
	for _, c := range []*clientConn{owner, peer} {
		f := behaviorFrame(t, c)
		if !slices.Equal(f.Panes, wantSplit) || len(f.Splitters) != 2 {
			t.Fatalf("unzoomed frame has panes %+v and %d splitters, want %+v and two splitters", f.Panes, len(f.Splitters), wantSplit)
		}
	}
	checkPaneSize(39, 12)

	s.handleClientMessage(clientMsg{owner, protocol.Resize{Cols: 100, Rows: 34}})
	s.handleClientMessage(clientMsg{peer, protocol.Resize{Cols: 120, Rows: 40}})
	wantOriginal := []protocol.PaneRect{
		{ID: 1, Number: 0, X: 0, Y: 0, W: 50, H: 33, Active: true},
		{ID: 2, Number: 1, X: 51, Y: 0, W: 49, H: 16},
		{ID: 3, Number: 2, X: 51, Y: 17, W: 49, H: 16},
	}
	for _, c := range []*clientConn{owner, peer} {
		f := behaviorFrame(t, c)
		if f.Cols != 100 || f.Rows != 34 || !slices.Equal(f.Panes, wantOriginal) {
			t.Fatalf("restored frame is %dx%d with panes %+v, want 100x34 and %+v", f.Cols, f.Rows, f.Panes, wantOriginal)
		}
		if got := cellAt(f, 51, 17).Ch; got != 'b' {
			t.Errorf("first restored bottom content cell = %q, want bottom pane marker", got)
		}
	}
	checkPaneSize(49, 16)
}
