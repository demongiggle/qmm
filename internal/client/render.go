package client

import (
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	doubleClickInterval = 500 * time.Millisecond
	doubleClickSlop     = 2
	// caretBlinkInterval is the half-cycle of the client-owned cursor
	// visibility toggle, independent of pane output and frame rate. The
	// native cursor uses steady shapes so tcell's per-redraw hide/show
	// cannot reset a terminal-controlled blink phase.
	caretBlinkInterval = 500 * time.Millisecond
)

// paneClick records a completed or pending click on a pane hit target.
type paneClick struct {
	x, y   int
	paneID int
	at     time.Time
}

// attach holds the interactive state of an attached client.
type attach struct {
	screen tcell.Screen
	codec  *protocol.Codec

	cols, rows int // terminal size reported to the server
	frameCols  int // size of the last frame the server sent us
	frameRows  int

	frame protocol.Frame // last frame, for mouse hit-testing

	// grid holds the unadorned server frame. It bounds cell updates and
	// detects stale screens; the caret is the host terminal's native cursor,
	// positioned locally over the grid.
	grid          []protocol.Cell
	gridCols      int
	gridRows      int
	caretX        int
	caretY        int
	caretVisible  bool
	caretShape    uint8
	caretBlink    bool
	caretBlinkOff bool // zero value means the native cursor is shown

	dragging     bool
	dragID       int
	dragVertical bool
	lastDragAxis int
	mouseDown    bool

	pendingClick paneClick
	lastClick    paneClick
}

// colorOf converts a protocol color to a tcell color. Palette indexes must
// go through tcell.PaletteColor: a bare tcell.Color(index) lacks ColorValid,
// so tcell silently omits it when writing SGR sequences to the terminal.
func colorOf(v int32) tcell.Color {
	switch {
	case v < 0:
		return tcell.ColorDefault
	case v&protocol.ColorRGBFlag != 0:
		return tcell.NewHexColor(v & 0xffffff)
	case v <= 255:
		return tcell.PaletteColor(int(v))
	default:
		return tcell.ColorDefault
	}
}

func styleOf(c protocol.Cell) tcell.Style {
	s := tcell.StyleDefault.Foreground(colorOf(c.FG)).Background(colorOf(c.BG))
	if c.Style&protocol.StyleBold != 0 {
		s = s.Bold(true)
	}
	if c.Style&protocol.StyleItalic != 0 {
		s = s.Italic(true)
	}
	if c.Style&protocol.StyleUnderline != 0 {
		s = s.Underline(true)
	}
	if c.Style&protocol.StyleBlink != 0 {
		s = s.Blink(true)
	}
	if c.Style&protocol.StyleReverse != 0 {
		s = s.Reverse(true)
	}
	return s
}

func blankClientCell(row, col int) protocol.Cell {
	return protocol.Cell{Row: row, Col: col, Ch: ' ', FG: protocol.ColorDefault, BG: protocol.ColorDefault}
}

func cellRune(ch rune) rune {
	if ch == 0 {
		return ' '
	}
	return ch
}

func (a *attach) resetGrid(cols, rows int) {
	a.gridCols, a.gridRows = cols, rows
	a.grid = make([]protocol.Cell, cols*rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			a.grid[y*cols+x] = blankClientCell(y, x)
		}
	}
}

// ensureGrid rebuilds the local copy after a full frame or a size change.
// It reports whether the baseline was rebuilt, so an incremental frame
// that arrives without a usable local grid can clear stale screen contents.
func (a *attach) ensureGrid(f protocol.Frame) bool {
	cols, rows := a.frameCols, a.frameRows
	if cols <= 0 || rows <= 0 {
		a.grid, a.gridCols, a.gridRows = nil, 0, 0
		return false
	}
	if !f.Full && a.gridCols == cols && a.gridRows == rows && len(a.grid) == cols*rows {
		return false
	}
	a.resetGrid(cols, rows)
	return true
}

func (a *attach) inGrid(x, y int) bool {
	return x >= 0 && y >= 0 && x < a.gridCols && y < a.gridRows
}

func (a *attach) setScreenCell(c protocol.Cell) {
	if !a.inGrid(c.Col, c.Row) {
		return
	}
	a.screen.SetContent(c.Col, c.Row, cellRune(c.Ch), nil, styleOf(c))
}

// nativeCursorShape maps the shared cursor-shape setting to a steady
// DECSCUSR style. Blink is owned by qmm's visibility ticker: using a
// terminal-controlled blinking style would let tcell's per-redraw
// hide/show restart the terminal's blink phase on every frame.
func nativeCursorShape(shape uint8) tcell.CursorStyle {
	switch shape {
	case protocol.CursorShapeUnderline:
		return tcell.CursorStyleSteadyUnderline
	case protocol.CursorShapeBar:
		return tcell.CursorStyleSteadyBar
	default: // protocol.CursorShapeBlock
		return tcell.CursorStyleSteadyBlock
	}
}

// syncCursor applies the native cursor's shape and visibility. It runs
// before screen.Show(), which flushes cursor state as part of its redraw.
func (a *attach) syncCursor() {
	a.screen.SetCursorStyle(nativeCursorShape(a.caretShape))
	if a.caretVisible && (!a.caretBlink || !a.caretBlinkOff) && a.inGrid(a.caretX, a.caretY) {
		a.screen.ShowCursor(a.caretX, a.caretY)
		return
	}
	a.screen.HideCursor()
}

// blinkCaret advances the native cursor's fixed-rate visibility phase.
// A steady cursor never requests a repaint, and hidden cursors never
// advance the phase.
func (a *attach) blinkCaret() bool {
	if !a.caretBlink || !a.caretVisible {
		return false
	}
	a.caretBlinkOff = !a.caretBlinkOff
	a.syncCursor()
	return true
}

func (a *attach) applyFrame(f protocol.Frame) {
	if f.Cols > 0 && f.Rows > 0 {
		a.frameCols, a.frameRows = f.Cols, f.Rows
	}
	cleared := false
	if f.Full {
		a.screen.Clear()
		cleared = true
	}
	if a.ensureGrid(f) && !cleared {
		a.screen.Clear()
	}
	for _, c := range f.Cells {
		if !a.inGrid(c.Col, c.Row) {
			continue
		}
		a.grid[c.Row*a.gridCols+c.Col] = c
		a.setScreenCell(c)
	}
	wasVisible := a.caretVisible
	styleChanged := a.caretShape != f.CursorShape || a.caretBlink != f.CursorBlink
	a.caretVisible = f.CursorVisible
	a.caretX, a.caretY = f.CursorX, f.CursorY
	a.caretShape = f.CursorShape
	a.caretBlink = f.CursorBlink
	// Style changes, a steady caret, and a cursor returning from an
	// application-hidden state all start on the shown phase. Ordinary output
	// frames leave a blinking caret's phase untouched.
	if styleChanged || (!wasVisible && a.caretVisible) || !a.caretBlink {
		a.caretBlinkOff = false
	}
	a.syncCursor()
	a.frame = f
}

// statusRow is the row index of the status bar, in frame coordinates.
func (a *attach) statusRow() int {
	if a.frameRows > 0 {
		return a.frameRows - 1
	}
	return a.rows - 1
}

func (a *attach) handleMouse(ev *tcell.EventMouse) {
	x, y := ev.Position()
	// Ignore input outside the server-rendered area (e.g. when this
	// terminal is bigger than the effective session size). A press there
	// can never start a gesture; a release ends one.
	if a.frameCols > 0 && a.frameRows > 0 && (x >= a.frameCols || y >= a.frameRows) {
		a.clearClicks()
		primary := ev.Buttons()&tcell.ButtonPrimary != 0
		switch {
		case !primary:
			a.mouseDown = false
			if a.dragging {
				_ = a.codec.Send(protocol.ActionDragEnd{Splitter: a.dragID})
				a.dragging = false
			}
		case !a.dragging:
			// Fresh press outside the frame: don't arm a gesture.
			a.mouseDown = false
		}
		return
	}
	pressed := ev.Buttons()&tcell.ButtonPrimary != 0

	switch {
	case pressed && !a.mouseDown:
		// Button press.
		a.mouseDown = true
		pane, onPane := a.paneAt(x, y)
		if onPane && a.isDoubleClick(x, y, pane.ID) {
			a.clearClicks()
			debugLog("double click pane=%d at %d,%d", pane.ID, x, y)
			_ = a.codec.Send(protocol.ActionTogglePaneZoom{PaneID: pane.ID})
			return
		}
		startedDrag := a.handlePress(x, y)
		if startedDrag || !onPane {
			a.clearClicks()
			return
		}
		a.pendingClick = paneClick{x: x, y: y, paneID: pane.ID, at: time.Now()}
	case pressed && a.mouseDown && a.dragging:
		// Drag in progress.
		axis := x
		if !a.dragVertical {
			axis = y
		}
		delta := axis - a.lastDragAxis
		if delta != 0 {
			debugLog("drag update splitter=%d delta=%d", a.dragID, delta)
			_ = a.codec.Send(protocol.ActionDragUpdate{Splitter: a.dragID, Delta: delta})
			a.lastDragAxis = axis
		}
		return
	case !pressed && a.mouseDown:
		// Release.
		a.mouseDown = false
		if a.dragging {
			_ = a.codec.Send(protocol.ActionDragEnd{Splitter: a.dragID})
			a.dragging = false
			a.clearClicks()
		} else if !a.pendingClick.at.IsZero() {
			a.lastClick = a.pendingClick
			a.lastClick.at = time.Now()
			a.pendingClick = paneClick{}
		}
	}
}

func (a *attach) clearClicks() {
	a.pendingClick = paneClick{}
	a.lastClick = paneClick{}
}

func (a *attach) isDoubleClick(x, y, paneID int) bool {
	c := a.lastClick
	if c.at.IsZero() || c.paneID != paneID || time.Since(c.at) > doubleClickInterval {
		return false
	}
	dx, dy := x-c.x, y-c.y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx <= doubleClickSlop && dy <= doubleClickSlop
}

// paneAt returns the topmost pane hit target at (x, y). A zoomed pane's
// floating overlay takes priority over the split panes underneath it.
func (a *attach) paneAt(x, y int) (protocol.PaneRect, bool) {
	for _, p := range a.frame.Panes {
		if p.Zoomed && paneRectContains(p, x, y) {
			return p, true
		}
	}
	for _, p := range a.frame.Panes {
		if paneRectContains(p, x, y) {
			return p, true
		}
	}
	return protocol.PaneRect{}, false
}

func paneRectContains(p protocol.PaneRect, x, y int) bool {
	return x >= p.X && x < p.X+p.W && y >= p.Y && y < p.Y+p.H
}

// handlePress handles a fresh mouse press; reports whether it started a drag.
func (a *attach) handlePress(x, y int) bool {
	// Status bar clicks: switch windows by clicking a tab.
	if y == a.statusRow() {
		for _, tab := range a.frame.Tabs {
			if x >= tab.Start && x < tab.End {
				_ = a.codec.Send(protocol.ActionSelectWindow{Index: tab.Index})
				return false
			}
		}
		return false
	}
	// Border drag: grab the splitter cell itself (clicking the adjacent pane
	// must focus it instead of starting a drag).
	debugLog("press at %d,%d; %d splitters", x, y, len(a.frame.Splitters))
	for _, sp := range a.frame.Splitters {
		var onBorder bool
		if sp.Vertical {
			onBorder = x == sp.X && y >= sp.Y && y < sp.Y+sp.Len
		} else {
			onBorder = y == sp.Y && x >= sp.X && x < sp.X+sp.Len
		}
		if !onBorder {
			continue
		}
		a.dragging = true
		a.dragID = sp.ID
		a.dragVertical = sp.Vertical
		if sp.Vertical {
			a.lastDragAxis = x
		} else {
			a.lastDragAxis = y
		}
		debugLog("drag start splitter=%d vertical=%v at %d", sp.ID, sp.Vertical, a.lastDragAxis)
		_ = a.codec.Send(protocol.ActionDragStart{Splitter: sp.ID})
		return true
	}
	// Otherwise: focus the pane under the cursor.
	_ = a.codec.Send(protocol.ActionFocus{X: x, Y: y})
	return false
}
