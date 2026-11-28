package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

func newDragServer(t *testing.T, direction layout.Direction) (*Server, *clientConn, layout.Border) {
	t.Helper()
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	w.tree = layout.New(1)
	if !w.tree.Split(1, 2, direction) {
		t.Fatal("split failed")
	}
	w.activePane = 2
	s.relayout()
	return s, c, w.tree.Borders(s.contentRect())[0]
}

func dragPaneRows(f *protocol.Frame) []string {
	r := f.Panes[1]
	rows := make([]string, r.H)
	for y := range rows {
		var row strings.Builder
		for x := 0; x < r.W; x++ {
			row.WriteRune(cellAt(*f, r.X+x, r.Y+y).Ch)
		}
		rows[y] = strings.TrimRight(row.String(), " ")
	}
	return rows
}

func TestSplitterDragRoundTripPreservesPane(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  layout.Direction
	}{
		{"top-bottom", layout.Horizontal},
		{"left-right", layout.Vertical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, border := newDragServer(t, tc.dir)
			p := s.sess.activePane()
			cols, rows := p.vt.Size()
			var lines []string
			if border.Vertical {
				lines = []string{strings.Repeat("x", cols-4) + "ABCD"}
			} else {
				for i := 1; i < rows; i++ {
					lines = append(lines, fmt.Sprintf("line-%02d", i))
				}
				lines = append(lines, "prompt$")
			}
			writePaneText(t, s, p.id, strings.Join(lines, "\r\n"))
			s.broadcast()
			initial := c.frame.Splitters[0]
			before := dragPaneRows(c.frame)
			if !slices.Equal(before[:len(lines)], lines) {
				t.Fatalf("initial pane rows = %q, want %q", before, lines)
			}

			s.handleClientMessage(clientMsg{c, protocol.ActionDragStart{Splitter: border.ID}})
			for range 4 {
				s.handleClientMessage(clientMsg{c, protocol.ActionDragUpdate{Splitter: border.ID, Delta: 1}})
			}
			want := initial
			if border.Vertical {
				want.X += 4
			} else {
				want.Y += 4
			}
			if got := c.frame.Splitters[0]; got != want {
				t.Fatalf("preview splitter = %+v, want %+v", got, want)
			}
			if c.frame.CursorVisible {
				t.Error("preview shows a cursor outside the clipped pane")
			}
			if w, h := p.vt.Size(); w != cols || h != rows {
				t.Fatalf("VT resized during preview to %dx%d, want %dx%d", w, h, cols, rows)
			}

			for range 4 {
				s.handleClientMessage(clientMsg{c, protocol.ActionDragUpdate{Splitter: border.ID, Delta: -1}})
			}
			s.handleClientMessage(clientMsg{c, protocol.ActionDragEnd{Splitter: border.ID}})
			if got := c.frame.Splitters[0]; got != initial {
				t.Fatalf("splitter after round trip = %+v, want %+v", got, initial)
			}
			if after := dragPaneRows(c.frame); !slices.Equal(after, before) {
				t.Fatalf("pane rows after round trip = %q, want %q", after, before)
			}
		})
	}
}

func TestSplitterDragCommitsPTYSizeOnRelease(t *testing.T) {
	s, c, border := newDragServer(t, layout.Horizontal)
	viewer := newHelpClient()
	s.clients[viewer] = struct{}{}
	p := s.sess.activePane()
	cols, rows := p.vt.Size()
	ptmx, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer slave.Close()
	p.ptmx = ptmx
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		t.Fatal(err)
	}
	checkSize := func(wantRows int) {
		t.Helper()
		if w, h := p.vt.Size(); w != cols || h != wantRows {
			t.Errorf("VT size = %dx%d, want %dx%d", w, h, cols, wantRows)
		}
		h, w, err := pty.Getsize(slave)
		if err != nil {
			t.Fatal(err)
		}
		if w != cols || h != wantRows {
			t.Errorf("PTY size = %dx%d, want %dx%d", w, h, cols, wantRows)
		}
	}

	s.handleClientMessage(clientMsg{c, protocol.ActionDragStart{Splitter: border.ID}})
	s.handleClientMessage(clientMsg{c, protocol.ActionDragUpdate{Splitter: border.ID, Delta: 100}})
	s.handleClientMessage(clientMsg{c, protocol.ActionDragUpdate{Splitter: border.ID, Delta: -96}})
	if got := c.frame.Splitters[0].Y; got != border.Y+4 {
		t.Fatalf("preview splitter row after reversing from the limit = %d, want %d", got, border.Y+4)
	}
	if got := viewer.frame.Splitters[0].Y; got != border.Y {
		t.Fatalf("viewer splitter row during preview = %d, want %d", got, border.Y)
	}
	checkSize(rows)

	s.handleClientMessage(clientMsg{c, protocol.ActionDragEnd{Splitter: border.ID}})
	checkSize(rows - 4)
	if got := viewer.frame.Splitters[0].Y; got != border.Y+4 {
		t.Fatalf("viewer splitter row after release = %d, want %d", got, border.Y+4)
	}
}

func TestSplitterDragCancelledByWindowSwitch(t *testing.T) {
	s, c, border := newDragServer(t, layout.Horizontal)
	s.sess.windows[1] = &window{index: 1, tree: layout.New(3), activePane: 3}
	s.handleClientMessage(clientMsg{c, protocol.ActionDragStart{Splitter: border.ID}})
	s.handleClientMessage(clientMsg{c, protocol.ActionDragUpdate{Splitter: border.ID, Delta: 4}})

	// Another client can switch the shared active window during the gesture.
	s.selectWindow(1)
	s.selectWindow(0)
	s.handleClientMessage(clientMsg{c, protocol.ActionDragEnd{Splitter: border.ID}})
	if got := c.frame.Splitters[0].Y; got != border.Y {
		t.Fatalf("splitter row after interrupted drag = %d, want %d", got, border.Y)
	}
}
