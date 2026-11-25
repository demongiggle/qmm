package server

import (
	"fmt"
	"time"

	"github.com/hinshun/vt10x"
	"github.com/rivo/uniseg"

	"github.com/demongiggle/cogito/internal/layout"
	"github.com/demongiggle/cogito/internal/protocol"
)

// vt10x glyph attribute bits (see vt10x state.go).
const (
	gttrReverse   = 1 << 0
	gttrUnderline = 1 << 1
	gttrBold      = 1 << 2
	gttrItalic    = 1 << 4
	gttrBlink     = 1 << 5
)

const (
	colDefault = protocol.ColorDefault

	colBorder       = 238
	colBorderActive = 46
	colBarBG        = 236
	colBarFG        = 248
	colBarActiveFG  = 16
	colBarActiveBG  = 46
	colTooSmall     = 203

	colHelpBackdrop = 233
	colHelpPanelBG  = 235
	colHelpBorder   = 39
	colHelpTitle    = 46
	colHelpHeader   = 81
	colHelpKeyFG    = 228
	colHelpKeyBG    = 238
	colHelpFG       = 252
	colHelpMuted    = 244
)

// compositor turns session state into protocol frames and diffs successive
// frames to produce incremental updates.
type compositor struct {
	lastCols, lastRows int
	last               []protocol.Cell
	haveLast           bool

	helpOpen bool
	helpTop  int
}

func blankCell(row, col int) protocol.Cell {
	return protocol.Cell{Row: row, Col: col, Ch: ' ', FG: colDefault, BG: colDefault}
}

// compose renders the current session state into a frame. It is called from
// the server's single event loop, so session state is stable while it runs.
func (c *compositor) compose(s *session, cols, rows int, forceFull bool) protocol.Frame {
	cells := make([]protocol.Cell, cols*rows)
	for i := range cells {
		cells[i] = blankCell(i/cols, i%cols)
	}
	setCell := func(x, y int, ch rune, fg, bg int32, style uint8) {
		if x < 0 || y < 0 || x >= cols || y >= rows {
			return
		}
		cells[y*cols+x] = protocol.Cell{Row: y, Col: x, Ch: ch, FG: fg, BG: bg, Style: style}
	}

	frame := protocol.Frame{Cols: cols, Rows: rows}
	render := func() protocol.Frame {
		if c.helpOpen {
			c.drawHelp(&frame, setCell, cols, rows)
		}
		return c.diff(cells, forceFull, frame)
	}

	if cols < minCols || rows < minRows {
		msg := fmt.Sprintf("terminal too small — need at least %dx%d", minCols, minRows)
		start := (cols - len([]rune(msg))) / 2
		if start < 0 {
			start = 0
		}
		for i, r := range []rune(msg) {
			setCell(start+i, rows/2, r, colTooSmall, colDefault, protocol.StyleBold)
		}
		frame.CursorVisible = false
		return render()
	}

	area := layout.Rect{X: 0, Y: 0, W: cols, H: rows - statusRows}
	w := s.activeWindow()

	var marks map[int]uint8 // cellIndex -> bitmask 1=vert 2=horiz

	var activeRect layout.Rect
	var haveActive bool

	if w != nil && w.zoomed {
		p := s.panes[w.activePane]
		// Zoom: pane fills the area inset by 1 cell, with a box border.
		inner := layout.Rect{X: 1, Y: 1, W: cols - 2, H: area.H - 2}
		activeRect, haveActive = inner, true
		if p != nil {
			blitPane(setCell, p, inner)
			c := p.vtCursor()
			frame.CursorX, frame.CursorY = inner.X+c.x, inner.Y+c.y
			frame.CursorVisible = c.visible
		}
		// box border; corners drawn with the horizontal edges, so the
		// verticals skip them.
		x0, y0, x1, y1 := 0, 0, cols-1, area.H-1
		for x := x0; x <= x1; x++ {
			setCell(x, y0, boxChar(x, x0, x1, '┌', '─', '┐'), colBorderActive, colDefault, 0)
			setCell(x, y1, boxChar(x, x0, x1, '└', '─', '┘'), colBorderActive, colDefault, 0)
		}
		for y := y0 + 1; y < y1; y++ {
			setCell(x0, y, '│', colBorderActive, colDefault, 0)
			setCell(x1, y, '│', colBorderActive, colDefault, 0)
		}
		frame.Splitters = nil // zoom has no drag handles
		frame.Panes = []protocol.PaneRect{{ID: w.activePane, X: inner.X, Y: inner.Y, W: inner.W, H: inner.H, Active: true}}
	} else if w != nil {
		rects := w.tree.Compute(area)
		borders := w.tree.Borders(area)

		for id, r := range rects {
			p := s.panes[id]
			if p == nil {
				continue
			}
			blitPane(setCell, p, r)
			if id == w.activePane {
				activeRect, haveActive = r, true
				c := p.vtCursor()
				frame.CursorX, frame.CursorY = r.X+c.x, r.Y+c.y
				frame.CursorVisible = c.visible
			}
			frame.Panes = append(frame.Panes, protocol.PaneRect{ID: id, X: r.X, Y: r.Y, W: r.W, H: r.H, Active: id == w.activePane})
		}

		// Draw borders; intersections become '+'. Track cell ownership.
		marks = map[int]uint8{}
		for _, b := range borders {
			for i := 0; i < b.Len; i++ {
				x, y := b.X, b.Y+i
				if !b.Vertical {
					x, y = b.X+i, b.Y
				}
				if x < 0 || y < 0 || x >= cols || y >= area.H {
					continue
				}
				idx := y*cols + x
				if b.Vertical {
					marks[idx] |= 1
				} else {
					marks[idx] |= 2
				}
			}
		}
		for idx, m := range marks {
			var ch rune
			switch m {
			case 1:
				ch = '│'
			case 2:
				ch = '─'
			default:
				ch = '┼'
			}
			x, y := idx%cols, idx/cols
			color := int32(colBorder)
			if haveActive && borderTouchesRect(x, y, m, activeRect) {
				color = colBorderActive
			}
			setCell(x, y, ch, color, colDefault, 0)
		}
		for _, b := range borders {
			frame.Splitters = append(frame.Splitters, protocol.Splitter{ID: b.ID, X: b.X, Y: b.Y, Len: b.Len, Vertical: b.Vertical})
		}
	}

	// Status bar.
	sbY := rows - 1
	for x := 0; x < cols; x++ {
		setCell(x, sbY, ' ', colBarFG, colBarBG, 0)
	}
	col := 0
	for _, idx := range s.sortedWindowIndices() {
		ww := s.windows[idx]
		label := fmt.Sprintf(" %d:%s", idx, ww.name)
		isActive := idx == s.active
		if isActive {
			label += "*"
			if ww.zoomed {
				label += "Z"
			}
		}
		label += " "
		runes := []rune(label)
		if col+len(runes) > cols-10 { // leave room for the clock
			truncated := string(runes[:max(0, cols-10-col)]) + "…"
			runes = []rune(truncated)
		}
		start := col
		for _, r := range runes {
			fg, bg := int32(colBarFG), int32(colBarBG)
			var st uint8
			if isActive {
				fg, bg = colBarActiveFG, colBarActiveBG
				st = protocol.StyleBold
			}
			setCell(col, sbY, r, fg, bg, st)
			col++
		}
		frame.Tabs = append(frame.Tabs, protocol.Tab{Index: idx, Start: start, End: col, Name: ww.name, Active: isActive, Zoomed: ww.zoomed})
		col++ // separator gap
		if col >= cols-10 {
			break
		}
	}
	clock := time.Now().Format("15:04:05")
	for i, r := range []rune(clock) {
		setCell(cols-len(clock)+i, sbY, r, colBarFG, colBarBG, 0)
	}

	return render()
}

func boxChar(x, x0, x1 int, left, mid, right rune) rune {
	switch x {
	case x0:
		return left
	case x1:
		return right
	default:
		return mid
	}
}

// borderTouchesRect reports whether the border cell at (x, y) lies on the
// edge of r. m is the cell's orientation bitmask (1 = vertical, 2 = horizontal).
func borderTouchesRect(x, y int, m uint8, r layout.Rect) bool {
	if m&1 != 0 { // vertical border cell
		if (x == r.Right() && y >= r.Y && y < r.Bottom()) || (x == r.X-1 && y >= r.Y && y < r.Bottom()) {
			return true
		}
	}
	if m&2 != 0 { // horizontal border cell
		if (y == r.Bottom() && x >= r.X && x < r.Right()) || (y == r.Y-1 && x >= r.X && x < r.Right()) {
			return true
		}
	}
	return false
}

type vtCursor struct {
	x, y    int
	visible bool
}

func (p *pane) vtCursor() vtCursor {
	p.vt.Lock()
	defer p.vt.Unlock()
	cur := p.vt.Cursor()
	vis := p.vt.CursorVisible()
	return vtCursor{x: cur.X, y: cur.Y, visible: vis}
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

// blitPane copies the pane's virtual screen into the cell buffer.
func blitPane(setCell func(x, y int, ch rune, fg, bg int32, style uint8), p *pane, r layout.Rect) {
	p.vt.Lock()
	cols, rows := p.vt.Size()
	for y := 0; y < r.H && y < rows; y++ {
		skip := 0
		for x := 0; x < r.W && x < cols; x++ {
			if skip > 0 {
				skip--
				continue
			}
			g := p.vt.Cell(x, y)
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			fg, bg := vtColor(g.FG), vtColor(g.BG)
			var st uint8
			if g.Mode&gttrBold != 0 {
				st |= protocol.StyleBold
			}
			if g.Mode&gttrItalic != 0 {
				st |= protocol.StyleItalic
			}
			if g.Mode&gttrUnderline != 0 {
				st |= protocol.StyleUnderline
			}
			if g.Mode&gttrBlink != 0 {
				st |= protocol.StyleBlink
			}
			if g.Mode&gttrReverse != 0 {
				st |= protocol.StyleReverse
			}
			if uniseg.StringWidth(string(ch)) == 2 {
				skip = 1
			}
			setCell(r.X+x, r.Y+y, ch, fg, bg, st)
		}
	}
	p.vt.Unlock()
}

// diff compares the new grid against the previous one and returns the frame.
func (c *compositor) diff(grid []protocol.Cell, forceFull bool, frame protocol.Frame) protocol.Frame {
	cols, rows := frame.Cols, frame.Rows
	sizeChanged := !c.haveLast || c.lastCols != cols || c.lastRows != rows || len(c.last) != len(grid)
	full := forceFull || sizeChanged

	var updates []protocol.Cell
	if full {
		updates = make([]protocol.Cell, len(grid))
		copy(updates, grid)
	} else {
		for i := range grid {
			if grid[i] != c.last[i] {
				updates = append(updates, grid[i])
			}
		}
		// If most of the screen changed anyway, send a full frame instead.
		if len(updates) > len(grid)/2 {
			updates = make([]protocol.Cell, len(grid))
			copy(updates, grid)
			full = true
		}
	}

	cp := make([]protocol.Cell, len(grid))
	copy(cp, grid)
	c.last = cp
	c.lastCols, c.lastRows = cols, rows
	c.haveLast = true

	frame.Full = full
	frame.Cells = updates
	return frame
}
