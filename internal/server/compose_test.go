package server

import (
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
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
	s.last = -1
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
	comp := &clientView{}
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
	comp := &clientView{}
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

	f := (&clientView{}).compose(s, 80, 24, true)

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
	// Both use the accent palette's default entry.
	if len(f.Tabs) == 0 {
		t.Fatal("missing status bar tabs")
	}
	tab := cellAt(f, f.Tabs[0].Start, 23)
	if tab.FG != colBarActiveFG || tab.BG != accentColorCodes[0] {
		t.Errorf("active tab colors = %d/%d, want %d/%d", tab.FG, tab.BG, colBarActiveFG, accentColorCodes[0])
	}
	border := cellAt(f, 40, 5)
	if border.FG != accentColorCodes[0] {
		t.Errorf("active border color = %d, want %d", border.FG, accentColorCodes[0])
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

	comp := &clientView{}
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

func TestComposeCarriesCursorStyleAcrossFrames(t *testing.T) {
	s := newTestSession()
	s.opts().setByID(settingCursorShape, 2)
	s.opts().setByID(settingCursorBlink, 0)
	comp := &clientView{}

	full := comp.compose(s, 80, 24, true)
	if full.CursorShape != protocol.CursorShapeBar || !full.CursorBlink {
		t.Fatalf("full frame cursor = shape:%d blink:%v, want blinking bar", full.CursorShape, full.CursorBlink)
	}
	incremental := comp.compose(s, 80, 24, false)
	if incremental.CursorShape != protocol.CursorShapeBar || !incremental.CursorBlink {
		t.Fatalf("incremental frame cursor = shape:%d blink:%v, want blinking bar", incremental.CursorShape, incremental.CursorBlink)
	}
	comp.setMenu(menuSettings)
	overlay := comp.compose(s, 80, 24, true)
	if overlay.CursorShape != protocol.CursorShapeBar || !overlay.CursorBlink {
		t.Fatalf("overlay frame cursor = shape:%d blink:%v, want blinking bar", overlay.CursorShape, overlay.CursorBlink)
	}
	if overlay.CursorVisible {
		t.Error("settings overlay should hide the pane cursor")
	}
}

func TestComposeEraseAboveRepaintsFirstLineBackground(t *testing.T) {
	s := newTestSession()
	p := s.panes[1]
	comp := &clientView{}

	// Initial screen with Normal background 235. The first line holds text that
	// must disappear when the application later erases above the cursor.
	if _, err := p.vt.Write([]byte("\x1b[m\x1b[48;5;235m\x1b[2JAAAA\r\nBBBB")); err != nil {
		t.Fatalf("write initial screen: %v", err)
	}
	_ = comp.compose(s, 80, 24, true)

	// The application switches to background 17, moves to the second line, and
	// uses erase-above (CSI 1J). That must repaint the whole first line with the
	// new background, not leave the old background behind.
	if _, err := p.vt.Write([]byte("\x1b[48;5;17m\x1b[2;2H\x1b[1J")); err != nil {
		t.Fatalf("write erase above: %v", err)
	}
	f := comp.compose(s, 80, 24, false)

	for x := 0; x < 39; x++ {
		got := cellAt(f, x, 0)
		if got.Ch != ' ' || got.BG != 17 {
			t.Errorf("first line cell (%d,0) = %+v, want blank with BG 17", x, got)
		}
	}
	for x := 0; x < 2; x++ {
		got := cellAt(f, x, 1)
		if got.Ch != ' ' || got.BG != 17 {
			t.Errorf("erased prefix cell (%d,1) = %+v, want blank with BG 17", x, got)
		}
	}
}

func TestComposePreservesBackgroundOnVimLineScroll(t *testing.T) {
	s := newTestSession()
	p := s.panes[1]
	comp := &clientView{}

	// Initial full-screen draw with Normal background 235 (matching vim's
	// startup / Ctrl+F / Ctrl+B clear sequence).
	if _, err := p.vt.Write([]byte("\x1b[m\x1b[38;5;252m\x1b[48;5;235m\x1b[H\x1b[2Jline1\r\nline2")); err != nil {
		t.Fatalf("write initial screen: %v", err)
	}
	_ = comp.compose(s, 80, 24, true)

	// Vim scrolls down by one line using DECSTBM + CUP to bottom of region + LF,
	// relying on xterm-256color BCE to fill the newly exposed row with BG 235
	// before writing only the non-blank prefix of the new line.
	if _, err := p.vt.Write([]byte("\x1b[1;22r\x1b[22;1H\r\n\x1b[1;23r\x1b[22;1Hshort")); err != nil {
		t.Fatalf("write vim line scroll: %v", err)
	}
	f := comp.compose(s, 80, 24, true)

	// Row 21 (1-based row 22) must have BG 235 on both the written text and
	// the trailing blank cells after it.
	for _, x := range []int{0, 4, 5, 20, 38} {
		got := cellAt(f, x, 21)
		if got.BG != 235 {
			t.Errorf("scrolled row cell (%d,21) = %+v, want BG 235", x, got)
		}
	}
}

func TestComposeWideGlyphHalfCellsKeepBackground(t *testing.T) {
	s := newTestSession()
	p := s.panes[1]
	comp := &clientView{}

	// Mixed double-width runes and plain ASCII with Normal background 235.
	// vt10x keeps one cell per rune: 日本語A行 sit in vt columns 0..5, but
	// 日, 本, 語 and 行 each occupy two terminal columns when shown.
	if _, err := p.vt.Write([]byte("\x1b[m\x1b[38;5;252m\x1b[48;5;235m\x1b[H\x1b[2J日本語 A行")); err != nil {
		t.Fatalf("write wide screen: %v", err)
	}
	f := comp.compose(s, 80, 24, true)

	// Every rune must be rendered, in order, at its expanded column. The old
	// skip logic dropped runes stored at odd vt columns next to a wide one.
	want := map[int]rune{0: '日', 2: '本', 4: '語', 6: ' ', 7: 'A', 8: '行'}
	for x, ch := range want {
		if got := cellAt(f, x, 0); got.Ch != ch {
			t.Errorf("cell (%d,0) = %q, want %q", x, got.Ch, ch)
		}
	}
	// The second half of each double-width rune keeps the glyph's colors
	// instead of falling back to a stray default-background gap.
	for _, x := range []int{1, 3, 5, 9} {
		if got := cellAt(f, x, 0); got.BG != 235 {
			t.Errorf("half cell (%d,0) BG = %d, want 235", x, got.BG)
		}
	}
	// Nothing else on the row may fall back to the default background.
	for x := 0; x < 39; x++ {
		if got := cellAt(f, x, 0); got.BG == protocol.ColorDefault {
			t.Errorf("cell (%d,0) fell back to default BG: %+v", x, got)
		}
	}
	// The caret column follows the same expansion as the glyphs: three wide
	// runes, one space and "A" fill columns 0..7, 行 covers 8..9.
	if f.CursorX != 10 || f.CursorY != 0 {
		t.Errorf("cursor at (%d,%d), want (10,0)", f.CursorX, f.CursorY)
	}
}

func TestComposeWideGlyphScrollKeepsBackground(t *testing.T) {
	s := newTestSession()
	p := s.panes[1]
	comp := &clientView{}

	// Initial full-screen draw with Normal background 235, then a real vim
	// DECSTBM line scroll: the freshly exposed row must keep BG 235, and the
	// scrolled CJK row above must keep its background on both halves of every
	// double-width rune.
	if _, err := p.vt.Write([]byte("\x1b[m\x1b[38;5;252m\x1b[48;5;235m\x1b[H\x1b[2J日本語\r\n中文行")); err != nil {
		t.Fatalf("write wide screen: %v", err)
	}
	if _, err := p.vt.Write([]byte("\x1b[1;22r\x1b[22;1H\r\n\x1b[1;23r\x1b[22;1H新行")); err != nil {
		t.Fatalf("write vim scroll: %v", err)
	}
	f := comp.compose(s, 80, 24, true)

	// The scroll shifts 中 文 行 onto row 0, and the freshly exposed bottom
	// row 21 holds 新行 with BG 235 on the runes, their half cells, and the
	// trailing blanks.
	want := map[[2]int]rune{{0, 0}: '中', {2, 0}: '文', {4, 0}: '行', {0, 21}: '新', {2, 21}: '行'}
	for pos, ch := range want {
		if got := cellAt(f, pos[0], pos[1]); got.Ch != ch {
			t.Errorf("cell (%d,%d) = %q, want %q", pos[0], pos[1], got.Ch, ch)
		}
	}
	for _, y := range []int{0, 21} {
		for x := 0; x < 39; x++ {
			c := cellAt(f, x, y)
			if c.BG == protocol.ColorDefault && c.Ch == ' ' {
				t.Errorf("cell (%d,%d) is a blank default-BG gap", x, y)
			}
		}
	}
}

// TestComposeWideGlyphRightEdgeClip keeps a double-width rune that straddles
// the pane's right edge from leaving a default-background gap behind.
func TestComposeWideGlyphRightEdgeClip(t *testing.T) {
	s := &session{
		windows:    map[int]*window{},
		panes:      map[int]*pane{},
		nextPaneID: 3,
		settings:   defaultSettings(),
		last:       -1,
	}
	tree := layout.New(1)
	tree.Split(1, 2, layout.Vertical)
	s.windows[0] = &window{index: 0, tree: tree, activePane: 1, name: "zsh"}
	s.active = 0
	// 40-column panes so the vt width and the pane rect agree: pane 1 covers
	// columns 0..39.
	s.panes[1] = &pane{id: 1, vt: vt10x.New(vt10x.WithSize(40, 23))}
	s.panes[2] = &pane{id: 2, vt: vt10x.New(vt10x.WithSize(40, 23))}
	comp := &clientView{}

	// 19 double-width runes fill columns 0..37, one space sits at 38, and a
	// 20th double-width rune straddles column 39.
	var row []rune
	for i := 0; i < 19; i++ {
		row = append(row, '日')
	}
	row = append(row, ' ')
	row = append(row, '語')
	if _, err := s.panes[1].vt.Write([]byte("\x1b[m\x1b[38;5;252m\x1b[48;5;235m\x1b[H\x1b[2J" + string(row))); err != nil {
		t.Fatalf("write clipped row: %v", err)
	}
	f := comp.compose(s, 80, 24, true)

	if got := cellAt(f, 39, 0); got.BG != 235 || got.Ch != ' ' {
		t.Errorf("clipped column (39,0) = %+v, want blank with BG 235", got)
	}
	// The caret expands past the clip too; it must clamp to the pane edge.
	if f.CursorX != 39 || f.CursorY != 0 {
		t.Errorf("cursor at (%d,%d), want clamped to (39,0)", f.CursorX, f.CursorY)
	}
}

// TestComposeWideGlyphCaretStaysInsidePane forces the compose call-site clamp:
// the vt is 60 columns while the pane rect is 40 wide, and 30 double-width
// runes expand the caret to column 60, which only the clamp at the compose
// call sites can bring back into the pane.
func TestComposeWideGlyphCaretStaysInsidePane(t *testing.T) {
	s := &session{
		windows:    map[int]*window{},
		panes:      map[int]*pane{},
		nextPaneID: 3,
		settings:   defaultSettings(),
		last:       -1,
	}
	tree := layout.New(1)
	tree.Split(1, 2, layout.Vertical)
	s.windows[0] = &window{index: 0, tree: tree, activePane: 1, name: "zsh"}
	s.active = 0
	s.panes[1] = &pane{id: 1, vt: vt10x.New(vt10x.WithSize(60, 23))}
	s.panes[2] = &pane{id: 2, vt: vt10x.New(vt10x.WithSize(60, 23))}
	comp := &clientView{}

	var row []rune
	for i := 0; i < 30; i++ {
		row = append(row, '日')
	}
	if _, err := s.panes[1].vt.Write([]byte("\x1b[m\x1b[H\x1b[2J" + string(row))); err != nil {
		t.Fatal(err)
	}
	f := comp.compose(s, 80, 24, true)
	if f.CursorX != 39 {
		t.Errorf("cursor at %d, want clamped to the pane edge (39)", f.CursorX)
	}
}

// TestSnapshotLineLayout keeps selection and copy-mode snapshots in the same
// terminal-column layout as the live pane: no dropped runes, consistent
// selection columns, and text that survives round-tripping.
func TestSnapshotLineLayout(t *testing.T) {
	// Snapshots and history share the live frame's terminal-column projection.
	p := &pane{vt: vt10x.New(vt10x.WithSize(39, 23))}
	if _, err := p.vt.Write([]byte("\x1b[m\x1b[38;5;252m\x1b[48;5;235m\x1b[H\x1b[2J日本語 A行")); err != nil {
		t.Fatal(err)
	}
	lines, curX, _ := p.vtSnapshot()
	row := lines[0]

	// The whole rendered layout: 日本語 fills columns 0..5, space at 6,
	// A at 7, 行 at 8..9. Placeholders carry Ch 0 and the rune's colors.
	want := map[int]rune{0: '日', 2: '本', 4: '語', 6: ' ', 7: 'A', 8: '行'}
	for x, ch := range want {
		if got := row.cells[x]; got.Ch != ch {
			t.Errorf("snapshot cell %d = %q, want %q", x, got.Ch, ch)
		}
	}
	for _, x := range []int{1, 3, 5, 9} {
		c := row.cells[x]
		if c.Ch != 0 || c.BG != 235 {
			t.Errorf("snapshot half cell %d = %+v, want placeholder with BG 235", x, c)
		}
	}
	// The expanded caret column matches the live pane's rendered caret.
	if curX != 10 {
		t.Errorf("snapshot caret at %d, want 10", curX)
	}

	// A straight blit puts the line on screen columns unchanged.
	set := map[[2]int]rune{}
	draw := func(x, y int, ch rune, fg, bg int32, st uint8) {
		set[[2]int{x, y}] = ch
	}
	blitSnapshotLine(draw, row, 10, 0, 20, nil, nil)
	for x, ch := range want {
		if got := set[[2]int{10 + x, 0}]; got != ch {
			t.Errorf("snapshotted cell at screen col %d = %q, want %q", 10+x, got, ch)
		}
	}
	if got, ok := set[[2]int{11, 0}]; !ok || got != ' ' {
		t.Errorf("snapshot half cell drawn at screen col 11 = %+v", set)
	}

	// Copy text skips placeholders: the full line round-trips.
	if got := row.text(); got != "日本語 A行" {
		t.Errorf("copy text = %q, want %q", got, "日本語 A行")
	}
}

// TestCopyLineColForRune converts a copy-text rune offset into the buffer
// column a search must place the cursor on: placeholders widen the line past
// the runes text() returns.
func TestCopyLineColForRune(t *testing.T) {
	line := copyLine{cells: []copyCell{
		{Ch: '日', FG: 1, BG: 2},
		{Ch: 0, FG: 1, BG: 2},
		{Ch: 'A', FG: 1, BG: 2},
		{Ch: ' ', FG: 1, BG: 2},
	}}
	if got := line.colForRune(0); got != 0 {
		t.Errorf("colForRune(0) = %d, want 0", got)
	}
	if got := line.colForRune(1); got != 2 {
		t.Errorf("colForRune(1) = %d, want 2 (A lives at column 2)", got)
	}
	// Past the last copyable rune; callers clamp to the line width.
	if got := line.colForRune(5); got != len(line.cells) {
		t.Errorf("colForRune(5) = %d, want %d", got, len(line.cells))
	}
}
