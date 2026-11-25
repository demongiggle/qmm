package server

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"

	"github.com/demongiggle/cogito/internal/layout"
	"github.com/demongiggle/cogito/internal/protocol"
)

func newTestSession() *session {
	s := &session{
		windows:    map[int]*window{},
		panes:      map[int]*pane{},
		nextPaneID: 3,
	}
	tree := layout.New(1)
	tree.Split(1, 2, layout.Vertical)
	s.windows[0] = &window{index: 0, tree: tree, activePane: 1, name: "zsh"}
	s.active = 0
	s.panes[1] = &pane{id: 1, vt: vt10x.New(vt10x.WithSize(39, 23))}
	s.panes[2] = &pane{id: 2, vt: vt10x.New(vt10x.WithSize(39, 23))}
	return s
}

func cellAt(f protocol.Frame, x, y int) protocol.Cell {
	for _, c := range f.Cells {
		if c.Col == x && c.Row == y {
			return c
		}
	}
	return protocol.Cell{Row: y, Col: x, Ch: '?'}
}

func TestComposeTooSmall(t *testing.T) {
	s := newTestSession()
	comp := &compositor{}
	f := comp.compose(s, 20, 5, true)
	var text strings.Builder
	for _, c := range f.Cells {
		if c.Ch != ' ' {
			text.WriteRune(c.Ch)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(text.String()), ""), "terminaltoosmall") {
		t.Fatalf("expected too-small notice, got %q", text.String())
	}
}

func TestComposeSplitFrame(t *testing.T) {
	s := newTestSession()
	comp := &compositor{}
	f := comp.compose(s, 80, 24, true)
	if !f.Full {
		t.Fatal("expected full frame")
	}
	// Vertical divider around column 39/40.
	mid := cellAt(f, 40, 5)
	if mid.Ch != '│' {
		t.Fatalf("expected border at (40,5), got %q", mid.Ch)
	}
	// Status bar shows the window list entry.
	found := false
	for _, c := range f.Cells {
		if c.Row == 23 && c.Ch == 'z' {
			found = true
		}
	}
	if !found {
		t.Fatal("status bar missing window name")
	}
	// Two panes reported.
	if len(f.Panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(f.Panes))
	}
	if len(f.Splitters) != 1 {
		t.Fatalf("got %d splitters, want 1", len(f.Splitters))
	}
}

func TestComposePreservesPaneAndWindowColors(t *testing.T) {
	s := newTestSession()
	if _, err := s.panes[1].vt.Write([]byte("\x1b[1;38;5;46;48;5;236mX")); err != nil {
		t.Fatalf("write 256-color pane output: %v", err)
	}
	if _, err := s.panes[2].vt.Write([]byte("\x1b[38;2;18;52;86;48;2;240;241;242mY")); err != nil {
		t.Fatalf("write RGB pane output: %v", err)
	}

	f := (&compositor{}).compose(s, 80, 24, true)

	// Pane output: 256-color palette and text attributes.
	paletteCell := cellAt(f, 0, 0)
	if paletteCell.Ch != 'X' || paletteCell.FG != 46 || paletteCell.BG != 236 {
		t.Fatalf("256-color pane cell = %+v, want X with FG 46 / BG 236", paletteCell)
	}
	if paletteCell.Style&protocol.StyleBold == 0 {
		t.Error("256-color pane cell lost bold attribute")
	}

	// Pane output: 24-bit RGB colors survive the protocol encoding.
	var second protocol.PaneRect
	for _, p := range f.Panes {
		if p.ID == 2 {
			second = p
		}
	}
	if second.W == 0 {
		t.Fatal("second pane missing from frame")
	}
	rgbCell := cellAt(f, second.X, second.Y)
	if want := protocol.RGBColor(18, 52, 86); rgbCell.FG != want {
		t.Errorf("RGB foreground = %#x, want %#x", rgbCell.FG, want)
	}
	if want := protocol.RGBColor(240, 241, 242); rgbCell.BG != want {
		t.Errorf("RGB background = %#x, want %#x", rgbCell.BG, want)
	}

	// Window chrome: the active tab and active pane border carry colors too.
	tab := cellAt(f, 0, 23)
	if tab.FG != colBarActiveFG || tab.BG != colBarActiveBG {
		t.Errorf("active tab colors = %d/%d, want %d/%d", tab.FG, tab.BG, colBarActiveFG, colBarActiveBG)
	}
	border := cellAt(f, 40, 5)
	if border.FG != colBorderActive {
		t.Errorf("active border color = %d, want %d", border.FG, colBorderActive)
	}
}

func TestComposeZoomFrame(t *testing.T) {
	s := newTestSession()
	s.windows[0].zoomed = true

	// Zoom relayout sizes the zoomed pane to the inner area.
	s.relayout(80, 23)
	if w, h := s.panes[1].vt.Size(); w != 78 || h != 21 {
		t.Fatalf("zoomed pane size = %dx%d, want 78x21", w, h)
	}

	comp := &compositor{}
	f := comp.compose(s, 80, 24, true)

	corners := []struct {
		x, y int
		want rune
	}{
		{0, 0, '┌'},
		{79, 0, '┐'},
		{0, 22, '└'},
		{79, 22, '┘'},
	}
	for _, c := range corners {
		got := cellAt(f, c.x, c.y).Ch
		if got != c.want {
			t.Errorf("corner (%d,%d) = %q, want %q", c.x, c.y, got, c.want)
		}
	}
	// Vertical edges
	for y := 1; y < 22; y++ {
		if got := cellAt(f, 0, y).Ch; got != '│' {
			t.Fatalf("left edge y=%d = %q, want │", y, got)
		}
		if got := cellAt(f, 79, y).Ch; got != '│' {
			t.Fatalf("right edge y=%d = %q, want │", y, got)
		}
	}
	// Status bar carries the zoom flag: "0:zsh*Z"
	hasZ := false
	for _, c := range f.Cells {
		if c.Row == 23 && c.Ch == 'Z' {
			hasZ = true
		}
	}
	if !hasZ {
		t.Fatal("status bar missing zoom flag")
	}
	// Exactly one pane exposed.
	if len(f.Panes) != 1 {
		t.Fatalf("got %d panes, want 1 when zoomed", len(f.Panes))
	}
}
