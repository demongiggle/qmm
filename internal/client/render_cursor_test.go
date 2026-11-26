package client

import (
	"reflect"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/qmm/internal/protocol"
)

// cursorStyleScreen records DECSCUSR styles because tcell's simulation
// screen deliberately ignores SetCursorStyle.
type cursorStyleScreen struct {
	tcell.SimulationScreen
	styles []tcell.CursorStyle
}

func (s *cursorStyleScreen) SetCursorStyle(style tcell.CursorStyle, _ ...tcell.Color) {
	s.styles = append(s.styles, style)
}

func newCursorStyleScreen(t *testing.T, cols, rows int) *cursorStyleScreen {
	t.Helper()
	return &cursorStyleScreen{SimulationScreen: newCaretScreen(t, cols, rows)}
}

func lastCursorStyle(t *testing.T, screen *cursorStyleScreen) tcell.CursorStyle {
	t.Helper()
	if len(screen.styles) == 0 {
		t.Fatal("SetCursorStyle was not called")
	}
	return screen.styles[len(screen.styles)-1]
}

func screenRunesAt(t *testing.T, screen tcell.SimulationScreen, x, y int) []rune {
	t.Helper()
	cells, width, height := screen.GetContents()
	if x < 0 || y < 0 || x >= width || y >= height {
		t.Fatalf("cell (%d,%d) outside screen %dx%d", x, y, width, height)
	}
	return append([]rune(nil), cells[y*width+x].Runes...)
}

func assertNativeCursor(t *testing.T, screen tcell.SimulationScreen, x, y int, visible bool) {
	t.Helper()
	gotX, gotY, gotVisible := screen.GetCursor()
	if gotVisible != visible || (visible && (gotX != x || gotY != y)) {
		t.Errorf("native cursor = (%d,%d,%v), want (%d,%d,%v)",
			gotX, gotY, gotVisible, x, y, visible)
	}
}

func TestCaretShapesUseNativeCursorStyles(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	blank := blankClientCell(0, 1)
	cases := []struct {
		name  string
		shape uint8
		want  tcell.CursorStyle
	}{
		{"block", protocol.CursorShapeBlock, tcell.CursorStyleSteadyBlock},
		{"underline", protocol.CursorShapeUnderline, tcell.CursorStyleSteadyUnderline},
		{"bar", protocol.CursorShapeBar, tcell.CursorStyleSteadyBar},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			screen := newCursorStyleScreen(t, 2, 1)
			a := &attach{screen: screen}
			a.applyFrame(protocol.Frame{
				Full: true, Cols: 2, Rows: 1,
				Cells:         []protocol.Cell{base, blank},
				CursorVisible: true, CursorShape: tc.shape,
			})
			screen.Show()

			if got := lastCursorStyle(t, screen); got != tc.want {
				t.Errorf("native cursor style = %v, want %v", got, tc.want)
			}
			if got := screenRunesAt(t, screen, 0, 0); !reflect.DeepEqual(got, []rune{'A'}) {
				t.Errorf("caret cell runes = %U, want [A]", got)
			}
			if got := screenStyleAt(t, screen, 0, 0); got != styleOf(base) {
				t.Errorf("caret cell style = %#v, want untouched %#v", got, styleOf(base))
			}
			assertNativeCursor(t, screen, 0, 0, true)
		})
	}
}

func TestCaretBlinkTogglesNativeCursorVisibility(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	screen := newCursorStyleScreen(t, 2, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blankClientCell(0, 1)},
		CursorVisible: true, CursorBlink: true,
	})
	screen.Show()

	if got := lastCursorStyle(t, screen); got != tcell.CursorStyleSteadyBlock {
		t.Errorf("blinking cursor style = %v, want steady block", got)
	}
	assertNativeCursor(t, screen, 0, 0, true)

	if !a.blinkCaret() {
		t.Fatal("blinkCaret did not request a repaint")
	}
	screen.Show()
	assertNativeCursor(t, screen, 0, 0, false)
	if got := screenStyleAt(t, screen, 0, 0); got != styleOf(base) {
		t.Errorf("blinked-off cell style = %#v, want untouched %#v", got, styleOf(base))
	}

	if !a.blinkCaret() {
		t.Fatal("blinkCaret did not request a repaint on the shown phase")
	}
	screen.Show()
	assertNativeCursor(t, screen, 0, 0, true)
}

func TestCaretOutsideFrameHidesNativeCursor(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	screen := newCursorStyleScreen(t, 2, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:   []protocol.Cell{base, blankClientCell(0, 1)},
		CursorX: 5, CursorY: 0, CursorVisible: true,
	})
	screen.Show()

	assertNativeCursor(t, screen, 5, 0, false)
	if got := lastCursorStyle(t, screen); got != tcell.CursorStyleSteadyBlock {
		t.Errorf("native cursor style = %v, want steady block", got)
	}
}

func TestApplicationHiddenCursorHidesNativeCursor(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	screen := newCursorStyleScreen(t, 2, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blankClientCell(0, 1)},
		CursorVisible: false, CursorBlink: true,
	})
	screen.Show()

	assertNativeCursor(t, screen, 0, 0, false)
	if a.blinkCaret() {
		t.Error("hidden cursor should not request a blink repaint")
	}
}

func TestCaretWideCharacterIsNotModified(t *testing.T) {
	wide := protocol.Cell{Row: 0, Col: 0, Ch: '界', FG: 46, BG: 236}
	base := []protocol.Cell{
		wide,
		blankClientCell(0, 1),
		{Row: 0, Col: 2, Ch: 'C', FG: 46, BG: 236},
	}
	screen := newCursorStyleScreen(t, 3, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 3, Rows: 1, Cells: base,
		CursorVisible: true, CursorShape: protocol.CursorShapeBar,
	})
	screen.Show()

	if got := screenRunesAt(t, screen, 0, 0); !reflect.DeepEqual(got, []rune{'界'}) {
		t.Fatalf("wide caret cell runes = %U, want [界]", got)
	}
	if got := screenStyleAt(t, screen, 0, 0); got != styleOf(wide) {
		t.Errorf("wide caret cell style = %#v, want untouched %#v", got, styleOf(wide))
	}
	assertNativeCursor(t, screen, 0, 0, true)

	a.applyFrame(protocol.Frame{
		Cols: 3, Rows: 1, CursorX: 2,
		CursorVisible: true, CursorShape: protocol.CursorShapeBar,
	})
	screen.Show()

	if got := screenRunesAt(t, screen, 0, 0); !reflect.DeepEqual(got, []rune{'界'}) {
		t.Errorf("old wide caret cell runes = %U, want [界]", got)
	}
	if got := screenStyleAt(t, screen, 0, 0); got != styleOf(wide) {
		t.Errorf("old wide caret cell style = %#v, want untouched %#v", got, styleOf(wide))
	}
	assertNativeCursor(t, screen, 2, 0, true)
}

func TestBlinkOffIsSteadyAndStyleChangeResetsPhase(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	screen := newCursorStyleScreen(t, 2, 1)
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
		t.Fatal("style change did not reset the blink phase to shown")
	}
	if got := lastCursorStyle(t, screen); got != tcell.CursorStyleSteadyUnderline {
		t.Errorf("changed cursor style = %v, want steady underline", got)
	}

	a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1, CursorVisible: true,
		CursorBlink: false, CursorShape: protocol.CursorShapeBlock,
	})
	if a.caretBlinkOff {
		t.Fatal("steady caret should always be shown")
	}
	if a.blinkCaret() {
		t.Error("steady caret should not request a blink repaint")
	}
	if got := lastCursorStyle(t, screen); got != tcell.CursorStyleSteadyBlock {
		t.Errorf("steady cursor style = %v, want steady block", got)
	}
}

func TestSteadyCaretClearsTextBlink(t *testing.T) {
	blinking := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236, Style: protocol.StyleBlink}
	blank := blankClientCell(0, 1)
	screen := newCursorStyleScreen(t, 2, 1)
	a := &attach{screen: screen}
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{blinking, blank},
		CursorVisible: true, CursorBlink: false,
	})
	screen.Show()

	// A steady caret must not inherit the terminal's slow text blink.
	wantSteady := styleOf(protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236})
	if got := screenStyleAt(t, screen, 0, 0); got != wantSteady {
		t.Errorf("steady caret cell style = %#v, want %#v (blink cleared)", got, wantSteady)
	}
	assertNativeCursor(t, screen, 0, 0, true)

	// Moving the caret restores the original blinking cell behind it.
	a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1, CursorX: 1,
		CursorVisible: true, CursorBlink: false,
	})
	screen.Show()
	if got := screenStyleAt(t, screen, 0, 0); got != styleOf(blinking) {
		t.Errorf("old caret cell style = %#v, want restored %#v", got, styleOf(blinking))
	}
	assertNativeCursor(t, screen, 1, 0, true)
}

func TestApplyFrameReportsNoOpFrames(t *testing.T) {
	base := protocol.Cell{Row: 0, Col: 0, Ch: 'A', FG: 46, BG: 236}
	blank := blankClientCell(0, 1)
	screen := newCursorStyleScreen(t, 2, 1)
	a := &attach{screen: screen}

	if !a.applyFrame(protocol.Frame{
		Full: true, Cols: 2, Rows: 1,
		Cells:         []protocol.Cell{base, blank},
		CursorX:       0,
		CursorY:       0,
		CursorVisible: true,
	}) {
		t.Fatal("initial full frame should report dirty")
	}

	// Empty incremental frame with identical cursor state is a no-op.
	if a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1,
		CursorX: 0, CursorY: 0, CursorVisible: true,
	}) {
		t.Error("empty incremental frame with unchanged cursor should not report dirty")
	}

	// Moving the cursor reports dirty.
	if !a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1,
		CursorX: 1, CursorY: 0, CursorVisible: true,
	}) {
		t.Error("cursor movement should report dirty")
	}

	// Changing a cell reports dirty.
	if !a.applyFrame(protocol.Frame{
		Cols: 2, Rows: 1,
		Cells:   []protocol.Cell{{Row: 0, Col: 1, Ch: 'B', FG: 46, BG: 236}},
		CursorX: 1, CursorY: 0, CursorVisible: true,
	}) {
		t.Error("cell update should report dirty")
	}
}
