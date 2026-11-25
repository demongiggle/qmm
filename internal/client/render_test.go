package client

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

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
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(2, 1)

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
