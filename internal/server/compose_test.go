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
		settings:   defaultSettings(),
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
	outer, inner := zoomRects(80, 23)

	s.relayout(80, 23)
	if w, h := s.panes[1].vt.Size(); w != inner.W || h != inner.H {
		t.Fatalf("zoomed pane size = %dx%d, want %dx%d", w, h, inner.W, inner.H)
	}
	// The split panes underneath keep their normal PTY sizes.
	if w, h := s.panes[2].vt.Size(); w != 39 || h != 23 {
		t.Fatalf("underlying pane size = %dx%d, want 39x23", w, h)
	}
	// Distinct output proves the underlying split remains visible in the
	// margin while the active pane is also blitted into the floating overlay.
	if _, err := s.panes[1].vt.Write([]byte("Z")); err != nil {
		t.Fatalf("write zoomed pane: %v", err)
	}
	if _, err := s.panes[2].vt.Write([]byte(strings.Repeat(" ", 38) + "R")); err != nil {
		t.Fatalf("write underlying pane: %v", err)
	}

	comp := &compositor{}
	f := comp.compose(s, 80, 24, true)

	corners := []struct {
		x, y int
		want rune
	}{
		{outer.X, outer.Y, '┌'},
		{outer.Right() - 1, outer.Y, '┐'},
		{outer.X, outer.Bottom() - 1, '└'},
		{outer.Right() - 1, outer.Bottom() - 1, '┘'},
	}
	for _, c := range corners {
		got := cellAt(f, c.x, c.y).Ch
		if got != c.want {
			t.Errorf("corner (%d,%d) = %q, want %q", c.x, c.y, got, c.want)
		}
	}
	for y := outer.Y + 1; y < outer.Bottom()-1; y++ {
		if got := cellAt(f, outer.X, y).Ch; got != '│' {
			t.Fatalf("left edge y=%d = %q, want │", y, got)
		}
		if got := cellAt(f, outer.Right()-1, y).Ch; got != '│' {
			t.Fatalf("right edge y=%d = %q, want │", y, got)
		}
	}
	if got := cellAt(f, 79, 0).Ch; got != 'R' {
		t.Errorf("underlying pane in right margin = %q, want R", got)
	}
	if got := cellAt(f, inner.X, inner.Y).Ch; got != 'Z' {
		t.Errorf("floating pane content = %q, want Z", got)
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
	// Two underlying hit targets plus the floating overlay are exposed.
	if len(f.Panes) != 3 {
		t.Fatalf("got %d panes, want 2 split panes plus the zoom overlay", len(f.Panes))
	}
	var overlays int
	for _, p := range f.Panes {
		if p.Zoomed {
			overlays++
			if p.ID != 1 || p.X != outer.X || p.Y != outer.Y || p.W != outer.W || p.H != outer.H || !p.Active {
				t.Errorf("zoom overlay rect = %+v, want ID 1 at %+v", p, outer)
			}
		} else if p.Active {
			t.Errorf("underlying pane %d is still active during zoom", p.ID)
		}
	}
	if overlays != 1 {
		t.Fatalf("got %d zoom overlays, want 1", overlays)
	}
	if len(f.Splitters) != 0 {
		t.Fatalf("got %d splitters, want none when zoomed", len(f.Splitters))
	}
}

func TestZoomRectsKeepCenteredTwoPercentMargins(t *testing.T) {
	for _, tc := range []struct{ cols, rows int }{{80, 23}, {200, 100}, {40, 9}} {
		outer, inner := zoomRects(tc.cols, tc.rows)
		wantX := max(1, (tc.cols*zoomMarginPercent+99)/100)
		wantY := max(1, (tc.rows*zoomMarginPercent+99)/100)
		if outer.X != wantX || outer.Y != wantY ||
			outer.Right() != tc.cols-wantX || outer.Bottom() != tc.rows-wantY {
			t.Errorf("zoomRects(%d,%d) outer = %+v, want %d/%d margins on each side",
				tc.cols, tc.rows, outer, wantX, wantY)
		}
		if inner.X != outer.X+1 || inner.Y != outer.Y+1 ||
			inner.W != outer.W-2 || inner.H != outer.H-2 {
			t.Errorf("zoomRects(%d,%d) inner = %+v, want one-cell inset from %+v",
				tc.cols, tc.rows, inner, outer)
		}
	}
}
