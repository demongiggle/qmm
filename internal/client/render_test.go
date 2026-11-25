package client

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

func newCaretScreen(t *testing.T, cols, rows int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	screen.SetSize(cols, rows)
	t.Cleanup(screen.Fini)
	return screen
}

func screenStyleAt(t *testing.T, screen tcell.SimulationScreen, x, y int) tcell.Style {
	t.Helper()
	cells, width, height := screen.GetContents()
	if x < 0 || y < 0 || x >= width || y >= height {
		t.Fatalf("cell (%d,%d) outside screen %dx%d", x, y, width, height)
	}
	return cells[y*width+x].Style
}

func TestColorOf(t *testing.T) {
	cases := []struct {
		name string
		in   int32
		want tcell.Color
	}{
		{"default", protocol.ColorDefault, tcell.ColorDefault},
		{"black palette entry", 0, tcell.PaletteColor(0)},
		{"green palette entry", 46, tcell.PaletteColor(46)},
		{"last palette entry", 255, tcell.PaletteColor(255)},
		{"24-bit RGB", protocol.RGBColor(18, 52, 86), tcell.NewHexColor(0x123456)},
		{"unknown negative", -2, tcell.ColorDefault},
		{"out of range", 256, tcell.ColorDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := colorOf(tc.in)
			if got != tc.want {
				t.Fatalf("colorOf(%d) = %#x, want %#x", tc.in, uint64(got), uint64(tc.want))
			}
			if tc.want != tcell.ColorDefault && !got.Valid() {
				t.Fatalf("colorOf(%d) produced invalid color %#x", tc.in, uint64(got))
			}
		})
	}
}

func TestApplyFrameRendersPaletteAndRGBColors(t *testing.T) {
	screen := newCaretScreen(t, 2, 1)

	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells: []protocol.Cell{
			{Row: 0, Col: 0, Ch: 'P', FG: 46, BG: 236, Style: protocol.StyleBold},
			{Row: 0, Col: 1, Ch: 'R', FG: protocol.RGBColor(18, 52, 86), BG: protocol.RGBColor(240, 241, 242), Style: protocol.StyleItalic},
		},
	})
	screen.Show()

	cells, width, height := screen.GetContents()
	if width != 2 || height != 1 || len(cells) != 2 {
		t.Fatalf("simulation screen = %dx%d with %d cells, want 2x1", width, height, len(cells))
	}
	want := []tcell.Style{
		tcell.StyleDefault.Foreground(tcell.PaletteColor(46)).Background(tcell.PaletteColor(236)).Bold(true),
		tcell.StyleDefault.Foreground(tcell.NewHexColor(0x123456)).Background(tcell.NewHexColor(0xf0f1f2)).Italic(true),
	}
	for i, wantStyle := range want {
		if cells[i].Style != wantStyle {
			t.Errorf("cell %d style = %#v, want %#v", i, cells[i].Style, wantStyle)
		}
	}
}

func TestApplyFrameDrawsSoftwareCaret(t *testing.T) {
	screen := newCaretScreen(t, 2, 1)
	a := &attach{screen: screen}
	base := []protocol.Cell{
		{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236, Style: protocol.StyleBold},
		{Row: 0, Col: 1, Ch: 'B', FG: protocol.RGBColor(18, 52, 86), BG: protocol.ColorDefault, Style: protocol.StyleItalic},
	}

	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1, Cells: base,
		CursorX: 1, CursorY: 0, CursorVisible: true,
	})
	screen.Show()

	if got, want := screenStyleAt(t, screen, 0, 0), styleOf(base[0]); got != want {
		t.Errorf("cell without caret style = %#v, want %#v", got, want)
	}
	caretCell := base[1]
	caretCell.Style |= protocol.StyleReverse
	if got, want := screenStyleAt(t, screen, 1, 0), styleOf(caretCell); got != want {
		t.Errorf("caret style = %#v, want %#v", got, want)
	}
	if _, _, visible := screen.GetCursor(); visible {
		t.Error("hardware cursor is visible; caret should be software-rendered")
	}
}

func TestCaretBlinkIsIndependentOfFrameRate(t *testing.T) {
	screen := newCaretScreen(t, 3, 1)
	a := &attach{screen: screen}
	base := []protocol.Cell{
		{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236, Style: protocol.StyleBold},
		{Row: 0, Col: 1, Ch: 'B', FG: 46, BG: 236, Style: protocol.StyleUnderline},
		{Row: 0, Col: 2, Ch: 'C', FG: 46, BG: 236},
	}
	caretCell := base[1]
	caretCell.Style |= protocol.StyleReverse
	caretOn := styleOf(caretCell)
	caretOff := styleOf(base[1])

	a.applyFrame(protocol.Frame{
		Full: true, Cols: 3, Rows: 1, Cells: base,
		CursorX: 1, CursorY: 0, CursorVisible: true,
	})
	screen.Show()
	if got := screenStyleAt(t, screen, 1, 0); got != caretOn {
		t.Fatalf("initial caret style = %#v, want %#v", got, caretOn)
	}

	// A burst of output frames must not change the caret phase.
	for i := 0; i < 20; i++ {
		changed := base[0]
		changed.Ch = rune('a' + i)
		a.applyFrame(protocol.Frame{
			Cols: 3, Rows: 1, Cells: []protocol.Cell{changed},
			CursorX: 1, CursorY: 0, CursorVisible: true,
		})
		screen.Show()
		if a.caretBlinkOff {
			t.Fatalf("output frame %d advanced the caret blink phase", i)
		}
		if got := screenStyleAt(t, screen, 1, 0); got != caretOn {
			t.Fatalf("caret on frame %d style = %#v, want %#v", i, got, caretOn)
		}
	}

	if !a.blinkCaret() {
		t.Fatal("blinkCaret did not report a visible caret repaint")
	}
	screen.Show()
	if got := screenStyleAt(t, screen, 1, 0); got != caretOff {
		t.Errorf("blinked-off caret style = %#v, want %#v", got, caretOff)
	}

	// Output while the phase is off must not turn the caret back on.
	for i := 0; i < 5; i++ {
		a.applyFrame(protocol.Frame{
			Cols: 3, Rows: 1,
			Cells:   []protocol.Cell{{Row: 0, Col: 2, Ch: rune('0' + i), FG: 46, BG: 236}},
			CursorX: 1, CursorY: 0, CursorVisible: true,
		})
		screen.Show()
		if got := screenStyleAt(t, screen, 1, 0); got != caretOff {
			t.Fatalf("caret off phase changed on output frame %d: %#v", i, got)
		}
	}

	a.blinkCaret()
	screen.Show()
	if got := screenStyleAt(t, screen, 1, 0); got != caretOn {
		t.Errorf("blinked-on caret style = %#v, want %#v", got, caretOn)
	}

	// An application-hidden cursor removes the software caret entirely.
	a.applyFrame(protocol.Frame{Cols: 3, Rows: 1, CursorVisible: false})
	screen.Show()
	if got := screenStyleAt(t, screen, 1, 0); got != caretOff {
		t.Errorf("hidden caret style = %#v, want %#v", got, caretOff)
	}
	if a.blinkCaret() {
		t.Error("hidden caret should not request a blink repaint")
	}
	if _, _, visible := screen.GetCursor(); visible {
		t.Error("hardware cursor became visible")
	}
}

func TestCaretMoveRestoresUnderlyingCellAndInvertsReverse(t *testing.T) {
	screen := newCaretScreen(t, 2, 1)
	a := &attach{screen: screen}
	base := []protocol.Cell{
		{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236, Style: protocol.StyleBold | protocol.StyleReverse},
		{Row: 0, Col: 1, Ch: 'B', FG: 46, BG: 236, Style: protocol.StyleUnderline},
	}

	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1, Cells: base,
		CursorX: 0, CursorY: 0, CursorVisible: true,
	})
	screen.Show()
	inverted := base[0]
	inverted.Style &^= protocol.StyleReverse
	if got, want := screenStyleAt(t, screen, 0, 0), styleOf(inverted); got != want {
		t.Fatalf("caret over reverse-video cell = %#v, want %#v", got, want)
	}

	a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1, CursorX: 1, CursorY: 0, CursorVisible: true,
	})
	screen.Show()
	if got, want := screenStyleAt(t, screen, 0, 0), styleOf(base[0]); got != want {
		t.Errorf("old caret cell = %#v, want restored %#v", got, want)
	}
	caret := base[1]
	caret.Style |= protocol.StyleReverse
	if got, want := screenStyleAt(t, screen, 1, 0), styleOf(caret); got != want {
		t.Errorf("new caret cell = %#v, want %#v", got, want)
	}
}
