package server

import (
	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

// The shutdown prompt takes tmux's prefix+& cue: a destructive close asks
// for an explicit y/n answer before it runs. qmm has only one session, so
// confirming ends the whole server rather than just one window.
const (
	confirmTitle    = " SHUTDOWN SESSION "
	confirmQuestion = "Kill session? (y/n)"
	confirmDetail   = "All windows and panes will close."
	confirmHint     = "y/Enter: shut down  n/Esc: cancel"
)

// openShutdownConfirm shows a modal dialog on the client that pressed the
// chord. Like help, the dialog is local so one attached user cannot cover
// another user's screen.
func (s *Server) openShutdownConfirm(c *clientConn) {
	if c.confirmOpen {
		return
	}
	c.helpOpen = false
	c.helpTop = 0
	c.settingsOpen = false
	c.sessionsOpen = false
	c.sessionsNaming = false
	c.resetLayoutsMenu()
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.confirmOpen = true
	c.forceFull = true
	s.broadcast()
}

func (s *Server) closeShutdownConfirm(c *clientConn) {
	if !c.confirmOpen {
		return
	}
	c.confirmOpen = false
	c.forceFull = true
	c.clearRepeat()
	s.broadcast()
}

// requestShutdown asks the event loop to tear the session down. Teardown is
// performed centrally by Run, after the current message has been handled.
func (s *Server) requestShutdown() {
	s.shutdownRequested = true
}

// handleShutdownConfirmInput consumes every key while the dialog is open.
// Yes ends the session; the familiar cancel keys dismiss it. Any other key
// is swallowed and rings the bell rather than reaching the pane below.
func (s *Server) handleShutdownConfirmInput(c *clientConn, data []byte) {
	switch string(data) {
	case "y", "Y", "\r", "\n":
		s.requestShutdown()
	case "n", "N", "q", "Q", "\x1b", "\x03":
		s.closeShutdownConfirm(c)
	default:
		c.enqueue(protocol.Notice{Text: "qmm: answer y or n"})
	}
}

// shutdownConfirmBox centers a compact dialog above the status bar. It stays
// readable at the smallest supported 40x10 terminal.
func shutdownConfirmBox(cols, rows int) (x0, y0, x1, y1 int) {
	w := min(cols-2, 58)
	if w < 10 {
		w = cols
	}
	h := min(rows-3, 7)
	if h < 3 {
		h = max(rows-1, 1)
	}
	x0 = max(0, (cols-w)/2)
	y0 = max(0, (rows-1-h)/2)
	x1 = min(cols-1, x0+w-1)
	y1 = min(rows-2, y0+h-1)
	if y1 < y0 {
		y1 = y0
	}
	return x0, y0, x1, y1
}

// drawShutdownConfirm renders an opaque modal over the composed frame. Pane
// hit targets and tabs are cleared so mouse input cannot change the session
// while the confirmation is pending.
func (c *compositor) drawShutdownConfirm(frame *protocol.Frame, setCell helpSetCell, cols, rows int) {
	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0
	frame.Panes = nil
	frame.Splitters = nil
	frame.Tabs = nil

	for y := 0; y < max(0, rows-statusRows); y++ {
		for x := 0; x < cols; x++ {
			setCell(x, y, ' ', c.chrome.help.muted, c.chrome.help.backdrop, 0)
		}
	}

	x0, y0, x1, y1 := shutdownConfirmBox(cols, rows)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			setCell(x, y, ' ', c.chrome.help.fg, c.chrome.help.panelBG, 0)
		}
	}
	drawConfirmFrame(setCell, x0, y0, x1, y1, c.chrome.help.panelBG, c.chrome.tooSmall)
	titleWidth := max(0, x1-x0-3)
	putHelpText(setCell, x0+2, y0, titleWidth, confirmTitle, c.chrome.tooSmall, c.chrome.help.panelBG, protocol.StyleBold)

	lines := []struct {
		text  string
		fg    int32
		style uint8
	}{
		{confirmQuestion, c.chrome.help.fg, protocol.StyleBold},
		{confirmDetail, c.chrome.help.fg, 0},
		{confirmHint, c.chrome.help.muted, 0},
	}
	bodyY := y0 + 2
	if y1-y0 < 6 {
		bodyY = y0 + 1
	}
	for i, line := range lines {
		y := bodyY + i
		if y >= y1 {
			break
		}
		putConfirmLine(setCell, x0, x1, y, line.text, line.fg, line.style, c.chrome.help.panelBG)
	}
}

func drawConfirmFrame(setCell helpSetCell, x0, y0, x1, y1 int, panelBG, border int32) {
	style := protocol.StyleBold
	for x := x0; x <= x1; x++ {
		setCell(x, y0, '─', border, panelBG, style)
		setCell(x, y1, '─', border, panelBG, style)
	}
	for y := y0; y <= y1; y++ {
		setCell(x0, y, '│', border, panelBG, style)
		setCell(x1, y, '│', border, panelBG, style)
	}
	setCell(x0, y0, '╭', border, panelBG, style)
	setCell(x1, y0, '╮', border, panelBG, style)
	setCell(x0, y1, '╰', border, panelBG, style)
	setCell(x1, y1, '╯', border, panelBG, style)
}

func putConfirmLine(setCell helpSetCell, x0, x1, y int, text string, fg int32, style uint8, panelBG int32) {
	width := max(0, x1-x0-3)
	textWidth := uniseg.StringWidth(text)
	offset := min(width, max(0, (width-textWidth)/2))
	putHelpText(setCell, x0+2+offset, y, width-offset, text, fg, panelBG, style)
}
