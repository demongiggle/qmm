package client

import (
	"reflect"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

type noCombiningScreen struct {
	tcell.SimulationScreen
}

func (s noCombiningScreen) CanDisplay(r rune, checkFallbacks bool) bool {
	if r == caretBarOverlay {
		return false
	}
	return s.SimulationScreen.CanDisplay(r, checkFallbacks)
}

func screenRunesAt(t *testing.T, screen tcell.SimulationScreen, x, y int) []rune {
	t.Helper()
	cells, width, height := screen.GetContents()
	if x < 0 || y < 0 || x >= width || y >= height {
		t.Fatalf("cell (%d,%d) outside screen %dx%d", x, y, width, height)
	}
	return append([]rune(nil), cells[y*width+x].Runes...)
}

func TestCaretShapesPreserveGlyphAndColors(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	blank := blankClientCell(0, 1)
	blockCell := base
	blockCell.Style = protocol.StyleReverse
	cases := []struct {
		name      string
		shape     uint8
		wantStyle tcell.Style
		wantRunes []rune
	}{
		{"block", protocol.CursorShapeBlock, styleOf(blockCell), []rune{'A'}},
		{"underline", protocol.CursorShapeUnderline, styleOf(base).Underline(tcell.UnderlineStyleSolid), []rune{'A'}},
		{"bar", protocol.CursorShapeBar, styleOf(base), []rune{'A', caretBarOverlay}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			screen := newCaretScreen(t, 2, 1)
			a := &attach{screen: screen}
			a.applyFrame(protocol.Frame{
				Full: true, Cols: 2, Rows: 1,
				Cells:         []protocol.Cell{base, blank},
				CursorVisible: true, CursorShape: tc.shape,
			})
			screen.Show()

			if got := screenStyleAt(t, screen, 0, 0); got != tc.wantStyle {
				t.Errorf("caret style = %#v, want %#v", got, tc.wantStyle)
			}
			if got := screenRunesAt(t, screen, 0, 0); !reflect.DeepEqual(got, tc.wantRunes) {
				t.Errorf("caret runes = %U, want %U", got, tc.wantRunes)
			}
			if _, _, visible := screen.GetCursor(); visible {
				t.Error("hardware cursor is visible; caret should be software-rendered")
			}
		})
	}
}

func TestCaretUnderlineDoublesExistingUnderline(t *testing.T) {
	base := protocol.Cell{
		Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236,
		Style: protocol.StyleUnderline | protocol.StyleBlink,
	}
	withoutBlink := base
	withoutBlink.Style &^= protocol.StyleBlink
	want := styleOf(withoutBlink).Underline(tcell.UnderlineStyleDouble)

	screen := newCaretScreen(t, 2, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blankClientCell(0, 1)},
		CursorVisible: true, CursorShape: protocol.CursorShapeUnderline,
	})
	screen.Show()

	if got := screenStyleAt(t, screen, 0, 0); got != want {
		t.Fatalf("double-underline caret style = %#v, want %#v", got, want)
	}
	if got := screenRunesAt(t, screen, 0, 0); !reflect.DeepEqual(got, []rune{'A'}) {
		t.Errorf("caret runes = %U, want [A]", got)
	}
}

func TestCaretBarFallsBackWhenOverlayUnsupported(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	sim := newCaretScreen(t, 2, 1)
	screen := noCombiningScreen{sim}
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blankClientCell(0, 1)},
		CursorVisible: true, CursorShape: protocol.CursorShapeBar,
	})
	sim.Show()

	if got, want := screenRunesAt(t, sim, 0, 0), []rune{caretBarFallback}; !reflect.DeepEqual(got, want) {
		t.Errorf("fallback caret runes = %U, want %U", got, want)
	}
	fallback := base
	fallback.Ch = caretBarFallback
	fallback.Style = protocol.StyleReverse
	if got, want := screenStyleAt(t, sim, 0, 0), styleOf(fallback); got != want {
		t.Errorf("fallback caret style = %#v, want %#v", got, want)
	}
}

func TestCaretBarPreservesAndRestoresWideCharacter(t *testing.T) {
	wide := protocol.Cell{Row: 0, Col: 0, Ch: '界', FG: 46, BG: 236}
	base := []protocol.Cell{wide, blankClientCell(0, 1), {Row: 0, Col: 2, Ch: 'C', FG: 46, BG: 236}}
	screen := newCaretScreen(t, 3, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 3, Rows: 1, Cells: base,
		CursorVisible: true, CursorShape: protocol.CursorShapeBar,
	})
	screen.Show()

	if got, want := screenRunesAt(t, screen, 0, 0), []rune{'界', caretBarOverlay}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wide bar caret runes = %U, want %U", got, want)
	}
	a.applyFrame(protocol.Frame{
		Cols: 3, Rows: 1, CursorX: 2,
		CursorVisible: true, CursorShape: protocol.CursorShapeBar,
	})
	screen.Show()

	if got, want := screenRunesAt(t, screen, 0, 0), []rune{'界'}; !reflect.DeepEqual(got, want) {
		t.Errorf("restored wide cell runes = %U, want %U", got, want)
	}
	if got, want := screenStyleAt(t, screen, 0, 0), styleOf(wide); got != want {
		t.Errorf("restored wide cell style = %#v, want %#v", got, want)
	}
}

func TestBlinkOffIsSteadyAndStyleChangeResetsPhase(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	screen := newCaretScreen(t, 2, 1)
	a := &attach{screen: screen}
	frame := protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blankClientCell(0, 1)},
		CursorVisible: true, CursorBlink: true,
	}
	a.applyFrame(frame)
	if !a.blinkCaret() || !a.caretBlinkOff {
		t.Fatal("blinking caret did not enter its off phase")
	}

	a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1, CursorVisible: true,
		CursorBlink: true, CursorShape: protocol.CursorShapeUnderline,
	})
	if a.caretBlinkOff {
		t.Fatal("style change did not reset the blink phase to lit")
	}

	a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1, CursorVisible: true,
		CursorBlink: false, CursorShape: protocol.CursorShapeBlock,
	})
	if a.caretBlinkOff {
		t.Fatal("steady caret should always be lit")
	}
	if a.blinkCaret() {
		t.Error("steady caret should not request a blink repaint")
	}
}
