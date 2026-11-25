package client

import (
	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

// attach holds the interactive state of an attached client.
type attach struct {
	screen tcell.Screen
	codec  *protocol.Codec

	cols, rows int // terminal size reported to the server
	frameCols  int // size of the last frame the server sent us
	frameRows  int

	frame protocol.Frame // last frame, for mouse hit-testing

	dragging     bool
	dragID       int
	dragVertical bool
	lastDragAxis int
	mouseDown    bool
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

func (a *attach) applyFrame(f protocol.Frame) {
	if f.Cols > 0 && f.Rows > 0 {
		a.frameCols, a.frameRows = f.Cols, f.Rows
	}
	if f.Full {
		a.screen.Clear()
	}
	for _, c := range f.Cells {
		if c.Row < 0 || c.Col < 0 {
			continue
		}
		ch := c.Ch
		if ch == 0 {
			ch = ' '
		}
		a.screen.SetContent(c.Col, c.Row, ch, nil, styleOf(c))
	}
	if f.CursorVisible {
		a.screen.ShowCursor(f.CursorX, f.CursorY)
	} else {
		a.screen.HideCursor()
	}
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
		if a.handlePress(x, y) {
			return
		}
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
		}
	}
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
