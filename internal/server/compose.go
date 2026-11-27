package server

import (
	"fmt"
	"sort"
	"time"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
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

	colBorder      = 238
	colBarBG       = 236
	colBarFG       = 248
	colBarActiveFG = 16
	colTooSmall    = 203

	colHelpBackdrop = 233
	colHelpPanelBG  = 235
	colHelpBorder   = 39
	colHelpTitle    = 46
	colHelpHeader   = 81
	colHelpKeyFG    = 228
	colHelpKeyBG    = 238
	colHelpFG       = 252
	colHelpMuted    = 244

	colConfirmBackdrop = colHelpBackdrop
	colConfirmPanelBG  = colHelpPanelBG
	colConfirmBorder   = colTooSmall
	colConfirmTitle    = colTooSmall
	colConfirmFG       = colHelpFG
	colConfirmMuted    = colHelpMuted
)

// compositor turns session state into protocol frames and diffs successive
// frames to produce incremental updates.
type compositor struct {
	lastCols, lastRows int
	last               []protocol.Cell
	haveLast           bool

	helpOpen bool
	helpTop  int

	confirmOpen bool

	configWarning     configWarning
	configWarningOpen bool

	displayPanes      bool
	displayPanesInput string

	settingsOpen   bool
	settingsCursor int
	settingsTop    int

	sessionsOpen    bool
	sessionsLoading bool
	sessionsList    []protocol.SessionInfo
	sessionsCursor  int
	sessionsTop     int
	sessionsNaming  bool
	sessionsInput   string
	sessionsError   string

	copyMode *copyModeState

	// selection is a mouse drag selection: a frozen snapshot of one pane that
	// is drawn in place while the rest of the layout stays live.
	selection *selectionState
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

	opts := s.opts()
	frame := protocol.Frame{
		Cols:        cols,
		Rows:        rows,
		CursorShape: opts.cursorShape(),
		CursorBlink: opts.cursorBlink(),
	}
	w := s.activeWindow()
	showClock := opts.showClock()
	activeColor := opts.borderColor()
	paneNumbers := make(map[int]int)
	for n, id := range s.windowPaneIDs(w) {
		paneNumbers[id] = n
	}
	render := func() protocol.Frame {
		if c.copyMode != nil && c.copyMode.open {
			c.drawCopyMode(setCell, c.copyMode)
		} else if c.configWarningOpen {
			c.drawConfigWarning(&frame, setCell, cols, rows, c.configWarning)
		} else if c.confirmOpen {
			c.drawShutdownConfirm(&frame, setCell, cols, rows)
		} else if c.helpOpen {
			c.drawHelp(&frame, setCell, cols, rows)
		} else if c.sessionsOpen {
			c.drawSessionsMenu(&frame, setCell, cols, rows, s.sessionName(), opts)
		} else if c.settingsOpen {
			c.drawSettings(&frame, setCell, cols, rows, opts)
		} else if c.displayPanes {
			c.drawDisplayPanes(&frame, setCell, cols, rows, showClock)
		}
		// Keep pane metadata deterministic despite the layout's rect map.
		// The client gives zoom overlays priority regardless of this order.
		sort.SliceStable(frame.Panes, func(i, j int) bool {
			a, b := frame.Panes[i], frame.Panes[j]
			if a.Number != b.Number {
				return a.Number < b.Number
			}
			if a.Zoomed != b.Zoomed {
				return b.Zoomed
			}
			return a.ID < b.ID
		})
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
		if c.copyMode != nil && c.copyMode.open {
			c.drawCopyStatusBar(setCell, opts, cols, rows, c.copyMode)
		}
		return render()
	}

	area := layout.Rect{X: 0, Y: 0, W: cols, H: rows - statusRows}

	var marks map[int]uint8 // cellIndex -> bitmask 1=vert 2=horiz

	var activeRect layout.Rect
	var haveActive bool

	if w != nil {
		rects := w.tree.Compute(area)
		borders := w.tree.Borders(area)

		for id, r := range rects {
			p := s.panes[id]
			if p == nil {
				continue
			}
			// A mouse selection shows a frozen snapshot of its pane, so the
			// highlight cannot drift while output keeps arriving. Everything
			// else on screen stays live.
			selected := c.selection != nil && c.selection.paneID == id && c.selection.rect == r
			if selected {
				c.drawSelection(setCell, c.selection, r)
			} else {
				blitPane(setCell, p, r)
			}
			if id == w.activePane {
				activeRect, haveActive = r, true
			}
			if id == w.activePane && !w.zoomed {
				cur := p.vtCursor()
				frame.CursorX, frame.CursorY = r.X+cur.x, r.Y+cur.y
				// A frozen snapshot has no live caret, and a copy view
				// replaces the pane's cells with scrollback.
				frame.CursorVisible = cur.visible && !selected && !c.copyModeCovers(id)
			}
			frame.Panes = append(frame.Panes, protocol.PaneRect{
				ID: id, Number: paneNumbers[id], X: r.X, Y: r.Y, W: r.W, H: r.H,
				Active: id == w.activePane && (!w.zoomed || c.displayPanes),
			})
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
				color = activeColor
			}
			setCell(x, y, ch, color, colDefault, 0)
		}
		if !w.zoomed {
			for _, b := range borders {
				frame.Splitters = append(frame.Splitters, protocol.Splitter{ID: b.ID, X: b.X, Y: b.Y, Len: b.Len, Vertical: b.Vertical})
			}
		}

		if w.zoomed {
			frame.Splitters = nil // the overlay covers the drag handles
			// While pane numbers are visible, show the underlying split
			// layout for this client instead of the floating zoom overlay.
			// Session PTY sizes stay unchanged; the display is transient.
			if !c.displayPanes {
				// Zoom is a floating overlay: the split panes and their borders
				// remain visible in a 2% margin around all four sides.
				outer, inner := zoomRects(area.W, area.H)
				for y := outer.Y; y < outer.Bottom(); y++ {
					for x := outer.X; x < outer.Right(); x++ {
						setCell(x, y, ' ', colDefault, colDefault, 0)
					}
				}
				p := s.panes[w.activePane]
				if p != nil {
					selected := c.selection != nil && c.selection.paneID == p.id && c.selection.rect == inner
					if selected {
						c.drawSelection(setCell, c.selection, inner)
					} else {
						blitPane(setCell, p, inner)
					}
					cur := p.vtCursor()
					frame.CursorX, frame.CursorY = inner.X+cur.x, inner.Y+cur.y
					// A frozen snapshot has no live caret, and a copy view
					// replaces the pane's cells with scrollback.
					frame.CursorVisible = cur.visible && !selected && !c.copyModeCovers(w.activePane)
				} else {
					frame.CursorVisible = false
				}
				// Box border; corners are drawn with the horizontal edges, so
				// the verticals skip them.
				x0, y0 := outer.X, outer.Y
				x1, y1 := outer.Right()-1, outer.Bottom()-1
				for x := x0; x <= x1; x++ {
					setCell(x, y0, boxChar(x, x0, x1, '┌', '─', '┐'), activeColor, colDefault, 0)
					setCell(x, y1, boxChar(x, x0, x1, '└', '─', '┘'), activeColor, colDefault, 0)
				}
				for y := y0 + 1; y < y1; y++ {
					setCell(x0, y, '│', activeColor, colDefault, 0)
					setCell(x1, y, '│', activeColor, colDefault, 0)
				}
				frame.Panes = append(frame.Panes, protocol.PaneRect{
					ID: w.activePane, Number: paneNumbers[w.activePane], X: outer.X, Y: outer.Y,
					W: outer.W, H: outer.H, Active: true, Zoomed: true,
				})
			}
		}
	}

	// The copy status line covers the session bar instead of stacking a second
	// status line under the split layout, so the copy view keeps the pane's full
	// height and the tabs/clock make way for the copy state.
	if c.copyMode != nil && c.copyMode.open {
		c.drawCopyStatusBar(setCell, opts, cols, rows, c.copyMode)
	} else {
		c.drawStatusBar(&frame, setCell, s, opts, cols, rows)
	}

	return render()
}

// drawStatusBar renders the bottom status bar (window tabs, optional clock)
// and records the tab hit zones in frame.Tabs. It is used for the live view, so
// a pending mouse selection keeps the same bar and tab hit zones.
func (c *compositor) drawStatusBar(frame *protocol.Frame, setCell func(x, y int, ch rune, fg, bg int32, style uint8), s *session, opts *settingsState, cols, rows int) {
	sbY := rows - 1
	showClock := opts.showClock()
	activeColor := opts.borderColor()
	bar := opts.statusBarStyle()
	for x := 0; x < cols; x++ {
		setCell(x, sbY, ' ', bar.fg, bar.bg, 0)
	}
	col := 0
	reserve := 0
	if showClock {
		reserve = displayPanesClockWidth + 2 // clock plus a small gap
	}
	if badge := fmt.Sprintf("[%s]", s.sessionName()); len(badge) > 0 && col < cols-reserve {
		badgeRunes := []rune(badge)
		if col+len(badgeRunes) > cols-reserve {
			badgeRunes = []rune(fitHelpText(badge, max(0, cols-reserve-col)))
		}
		for _, r := range badgeRunes {
			setCell(col, sbY, r, bar.accent, bar.bg, protocol.StyleBold)
			col++
		}
		if col < cols-reserve {
			col++ // separator gap before window tabs
		}
	}
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
		if col+len(runes) > cols-reserve {
			truncated := string(runes[:max(0, cols-reserve-col)]) + "…"
			runes = []rune(truncated)
		}
		start := col
		for _, r := range runes {
			fg, bg := bar.fg, bar.bg
			var st uint8
			if isActive {
				fg, bg = colBarActiveFG, activeColor
				st = protocol.StyleBold
			}
			setCell(col, sbY, r, fg, bg, st)
			col++
		}
		frame.Tabs = append(frame.Tabs, protocol.Tab{Index: idx, Start: start, End: col, Name: ww.name, Active: isActive, Zoomed: ww.zoomed})
		col++ // separator gap
		if col >= cols-reserve {
			break
		}
	}
	if showClock {
		clock := time.Now().Format("15:04:05")
		for i, r := range []rune(clock) {
			setCell(cols-len(clock)+i, sbY, r, bar.fg, bar.bg, 0)
		}
	}
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
