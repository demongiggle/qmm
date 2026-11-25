package client

import (
	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

// attach holds the interactive state of an attached client.
type attach struct {
	screen tcell.Screen
	codec  *protocol.Codec

	cols, rows int

	frame protocol.Frame // last frame, for mouse hit-testing

	dragging     bool
	dragID       int
	dragVertical bool
	lastDragAxis int
	mouseDown    bool
}

func colorOf(v int16) tcell.Color {
	if v < 0 {
		return tcell.ColorDefault
	}
	return tcell.Color(v)
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

// statusRow is the row index of the status bar.
func (a *attach) statusRow() int { return a.rows - 1 }

func (a *attach) handleMouse(ev *tcell.EventMouse) {
	x, y := ev.Position()
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
	// Border drag: grab a splitter (1-cell tolerance).
	for _, sp := range a.frame.Splitters {
		var dist int
		if sp.Vertical {
			dist = abs(x - sp.X)
		} else {
			dist = abs(y - sp.Y)
		}
		if dist <= 1 {
			// Within the splitter's span?
			inSpan := false
			if sp.Vertical {
				inSpan = y >= sp.Y-1 && y <= sp.Y+sp.Len
			} else {
				inSpan = x >= sp.X-1 && x <= sp.X+sp.Len
			}
			if !inSpan {
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
			_ = a.codec.Send(protocol.ActionDragStart{Splitter: sp.ID})
			return true
		}
	}
	// Otherwise: focus the pane under the cursor.
	_ = a.codec.Send(protocol.ActionFocus{X: x, Y: y})
	return false
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
