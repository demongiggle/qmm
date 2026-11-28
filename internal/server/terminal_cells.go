package server

import (
	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
)

// projectVTRow translates one locked VT row into terminal columns. A drag
// preview can be narrower or wider than the VT, so the number of source
// glyphs and the destination width are independent. A clipped glyph is a
// styled space; a wide glyph's continuation has Ch == 0.
func projectVTRow(vt vt10x.View, y, sourceCols, width int, emit func(int, copyCell)) {
	col := 0
	for x := 0; x < sourceCols && col < width; x++ {
		g := vt.Cell(x, y)
		cell := copyCell{Ch: g.Char, FG: vtColor(g.FG), BG: vtColor(g.BG), Style: vtStyle(g.Mode)}
		if cell.Ch == 0 {
			cell.Ch = ' '
		}
		span := vtRuneWidth(cell.Ch)
		if col+span > width {
			span = width - col
			cell.Ch = ' '
		}
		emit(col, cell)
		cell.Ch = 0
		for offset := 1; offset < span; offset++ {
			emit(col+offset, cell)
		}
		col += span
	}
}

func vtStyle(mode int16) uint8 {
	var style uint8
	if mode&vt10x.AttrReverse != 0 {
		style |= protocol.StyleReverse
	}
	if mode&vt10x.AttrUnderline != 0 {
		style |= protocol.StyleUnderline
	}
	if mode&vt10x.AttrBold != 0 {
		style |= protocol.StyleBold
	}
	if mode&vt10x.AttrItalic != 0 {
		style |= protocol.StyleItalic
	}
	if mode&vt10x.AttrBlink != 0 {
		style |= protocol.StyleBlink
	}
	return style
}

type vtCursor struct {
	x, y    int
	visible bool
}

// vtRuneWidth reports how many terminal columns a rune occupies. vt10x stores
// one cell per rune regardless of width, and combining marks on their own
// still have to claim a column, so anything below one counts as one.
func vtRuneWidth(ch rune) int {
	w := uniseg.StringWidth(string(ch))
	if w < 1 {
		return 1
	}
	return w
}

func (p *pane) vtCursor() vtCursor {
	p.vt.Lock()
	defer p.vt.Unlock()
	cur := p.vt.Cursor()
	vis := p.vt.CursorVisible()
	// blitPane expands double-width runes into two columns, so the caret
	// column must go through the same expansion or it lands on the wrong
	// cell whenever the row contains wide characters.
	return vtCursor{x: renderedCursorColumn(p.vt, cur.X, cur.Y), y: cur.Y, visible: vis}
}

// vtColor translates a vt10x color into the protocol's color encoding.
// vt10x reserves values at and above DefaultFG for default colors, stores
// palette indexes below 256, and stores 24-bit RGB values as 0xRRGGBB. Very
// dark RGB values are indistinguishable from palette indexes in vt10x, so they
// retain their palette interpretation.
func vtColor(c vt10x.Color) int32 {
	switch {
	case c >= vt10x.DefaultFG:
		return protocol.ColorDefault
	case c <= 0xff:
		return int32(c)
	default:
		return protocol.RGBColor(int32(c>>16), int32(c>>8), int32(c))
	}
}

// vtSnapshot copies the pane's current vt10x screen and cursor position.
// Lines are expressed in terminal columns, the same space the scrollback
// recorder uses: a double-width rune owns two cells and a zero-rune
// placeholder marks its second half. The whole copy buffer then shares one
// coordinate system, so a screen column maps 1:1 onto a buffer column.
func (p *pane) vtSnapshot() ([]copyLine, int, int) {
	p.vt.Lock()
	defer p.vt.Unlock()
	cols, rows := p.vt.Size()
	cur := p.vt.Cursor()
	out := make([]copyLine, rows)
	for y := 0; y < rows; y++ {
		cells := make([]copyCell, cols)
		projectVTRow(p.vt, y, cols, cols, func(x int, cell copyCell) {
			cells[x] = cell
		})
		out[y] = copyLine{cells: cells}
	}
	return out, renderedCursorColumn(p.vt, cur.X, cur.Y), cur.Y
}

// renderedCursorColumn maps a vt10x cursor column onto the terminal-column
// layout the copy buffer and the pane renderer use. vt10x advances one cell
// per rune, so the caret must be expanded by the widths of the runes before
// it. Callers must hold the vt lock.
func renderedCursorColumn(vt vt10x.Terminal, col, row int) int {
	cols, rows := vt.Size()
	if row < 0 || row >= rows {
		return col
	}
	x := 0
	for i := 0; i < col && i < cols; i++ {
		ch := vt.Cell(i, row).Char
		if ch == 0 {
			ch = ' '
		}
		x += vtRuneWidth(ch)
	}
	return clampInt(x, 0, cols-1)
}
