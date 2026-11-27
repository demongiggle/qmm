package server

import (
	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	configWarningTitle = " CONFIGURATION WARNING "
	configWarningHint  = "q/Esc/Enter/Space/Ctrl-c dismiss"
)

// showConfigWarning assigns a blocking, client-local recovery dialog. Startup
// warnings are queued on the server and assigned to the first attached client;
// runtime warnings go straight to the client that triggered the failure.
func (s *Server) showConfigWarning(c *clientConn, warning configWarning) {
	if c == nil {
		return
	}
	c.configWarning = warning
	c.configWarningOpen = true
	c.resetLayoutsMenu()
	c.forceFull = true
}

func (s *Server) closeConfigWarning(c *clientConn) {
	if !c.configWarningOpen {
		return
	}
	c.configWarningOpen = false
	c.configWarning = configWarning{}
	c.forceFull = true
	s.broadcast()
}

func (s *Server) handleConfigWarningInput(c *clientConn, data []byte) {
	switch string(data) {
	case "q", "Q", "\x1b", "\x03", "\r", "\n", " ":
		s.closeConfigWarning(c)
	default:
		c.enqueue(protocol.Notice{Text: "qmm: dismiss config warning"})
	}
}

func (w configWarning) lines() []string {
	backup := w.BackupPath
	if backup == "" {
		backup = "(none)"
	}
	return []string{
		"Reason: " + w.Reason,
		"Backup: " + backup,
		"Config: " + w.ConfigPath,
		configWarningHint,
	}
}

// drawConfigWarning renders an opaque modal above every other overlay. It
// uses the confirmation box geometry so the reason and paths stay readable at
// the smallest supported terminal size.
func (c *compositor) drawConfigWarning(frame *protocol.Frame, setCell helpSetCell, cols, rows int, warning configWarning) {
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
	putHelpText(setCell, x0+2, y0, titleWidth, configWarningTitle, c.chrome.tooSmall, c.chrome.help.panelBG, protocol.StyleBold)

	width := max(0, x1-x0-3)
	bodyY := y0 + 2
	if y1-y0 < 6 {
		bodyY = y0 + 1
	}
	for i, line := range warning.lines() {
		y := bodyY + i
		if y >= y1 {
			break
		}
		fg, style := c.chrome.help.fg, uint8(0)
		switch i {
		case 0:
			fg, style = c.chrome.tooSmall, protocol.StyleBold
		case 1, 2:
			fg = c.chrome.help.keyFG
		default:
			fg = c.chrome.help.muted
		}
		putHelpText(setCell, x0+2, y, width, fitHelpText(line, width), fg, c.chrome.help.panelBG, style)
	}
}
