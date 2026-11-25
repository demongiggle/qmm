package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	// displayPanesTimeout is long enough to read the badges and press a
	// number, but short enough that the overlay disappears on its own.
	displayPanesTimeout = 2 * time.Second
	// displayPanesDigitTimeout extends the overlay briefly after an
	// ambiguous first digit, so pane 10 can be entered as "10".
	displayPanesDigitTimeout = time.Second
	// displayPanesExpireInterval bounds how late an expired overlay can
	// linger on screen. The event loop only inspects state, never renders
	// from a timer goroutine.
	displayPanesExpireInterval = 100 * time.Millisecond
	displayPanesClockWidth     = len("15:04:05")
)

const (
	colPaneNumberFG       int32 = 228
	colPaneNumberBG       int32 = 236
	colPaneNumberActiveFG int32 = 16
	colPaneNumberActiveBG int32 = 46
	colPaneNumberHintFG   int32 = 16
	colPaneNumberHintBG   int32 = 46
)

// Each glyph is a 3x5 mask. The marked cells are drawn with the digit's own
// rune, so the large number stays recognizable in a cell-only terminal.
var paneNumberGlyphs = [10][5]string{
	{"###", "# #", "# #", "# #", "###"}, // 0
	{"  #", " ##", "  #", "  #", "###"}, // 1
	{"###", "  #", "###", "#  ", "###"}, // 2
	{"###", "  #", "###", "  #", "###"}, // 3
	{"# #", "# #", "###", "  #", "  #"}, // 4
	{"###", "#  ", "###", "  #", "###"}, // 5
	{"###", "#  ", "###", "# #", "###"}, // 6
	{"###", "  #", "  #", "  #", "  #"}, // 7
	{"###", "# #", "###", "# #", "###"}, // 8
	{"###", "# #", "###", "  #", "###"}, // 9
}

// openDisplayPanes shows tmux-style pane numbers on one client. The numbers
// are a transient, per-client overlay; the session layout is left untouched.
func (s *Server) openDisplayPanes(c *clientConn) {
	if s.paneCount() == 0 {
		return
	}
	c.helpOpen = false
	c.helpTop = 0
	c.confirmOpen = false
	c.settingsOpen = false
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.displayPanesOpen = true
	c.displayPanesInput = ""
	c.displayPanesDeadline = time.Now().Add(s.sess.opts().paneNumberTimeoutDuration())
	c.forceFull = true
	s.broadcast()
}

func (s *Server) closeDisplayPanes(c *clientConn) {
	if !c.displayPanesOpen {
		return
	}
	c.displayPanesOpen = false
	c.displayPanesInput = ""
	c.displayPanesDeadline = time.Time{}
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.forceFull = true
	s.broadcast()
}

func (s *Server) paneCount() int {
	w := s.activeWindow()
	if w == nil || w.tree == nil {
		return 0
	}
	return w.tree.Len()
}

func (s *Server) displayPanesExpired(c *clientConn) bool {
	return c.displayPanesOpen && !time.Now().Before(c.displayPanesDeadline)
}

func (s *Server) expireDisplayPanes() {
	for c := range s.clients {
		s.expireDisplayPanesFor(c)
	}
}

func (s *Server) expireDisplayPanesFor(c *clientConn) {
	if !s.displayPanesExpired(c) {
		return
	}
	if c.displayPanesInput != "" {
		s.commitDisplayPanesInput(c, s.paneCount())
		return
	}
	s.closeDisplayPanes(c)
}

// handleDisplayPanesInput consumes keys while the numbers are visible.
// A digit selects a pane immediately unless it could start a longer pane
// number; Enter commits an ambiguous prefix, and other keys cancel.
func (s *Server) handleDisplayPanesInput(c *clientConn, data []byte) {
	count := s.paneCount()
	if count == 0 {
		s.closeDisplayPanes(c)
		return
	}
	for _, b := range data {
		switch {
		case b >= '0' && b <= '9':
			if s.appendDisplayPanesDigit(c, int(b-'0'), count) {
				return
			}
		case b == '\r' || b == '\n':
			s.commitDisplayPanesInput(c, count)
			return
		case b == 0x7f || b == 0x08:
			if len(c.displayPanesInput) == 0 {
				continue
			}
			c.displayPanesInput = c.displayPanesInput[:len(c.displayPanesInput)-1]
		case b == 0x1b || b == 0x03 || b == 'q' || b == 'Q':
			s.closeDisplayPanes(c)
			return
		default:
			// The overlay is modal: cancel rather than letting a stray
			// command reach the shell underneath.
			s.closeDisplayPanes(c)
			return
		}
	}
	s.refreshDisplayPanesDeadline(c)
}

// appendDisplayPanesDigit returns true when the overlay has closed, either
// because the number selected a pane or because the input became invalid.
func (s *Server) appendDisplayPanesDigit(c *clientConn, digit, count int) bool {
	maxNumber := count - 1
	maxDigits := len(strconv.Itoa(maxNumber))
	next := c.displayPanesInput + strconv.Itoa(digit)
	if len(next) > maxDigits || !paneNumberHasPrefix(next, count) {
		c.enqueue(protocol.Notice{Text: "qmm: invalid pane number"})
		s.closeDisplayPanes(c)
		return true
	}
	c.displayPanesInput = next
	if paneNumberHasLongerMatch(next, count) {
		return false
	}
	n, _ := strconv.Atoi(next)
	s.selectDisplayPane(c, n)
	s.closeDisplayPanes(c)
	return true
}

func (s *Server) commitDisplayPanesInput(c *clientConn, count int) {
	if c.displayPanesInput == "" {
		s.closeDisplayPanes(c)
		return
	}
	n, err := strconv.Atoi(c.displayPanesInput)
	if err != nil || n < 0 || n >= count {
		c.enqueue(protocol.Notice{Text: "qmm: invalid pane number"})
	} else {
		s.selectDisplayPane(c, n)
	}
	s.closeDisplayPanes(c)
}

func (s *Server) refreshDisplayPanesDeadline(c *clientConn) {
	if c.displayPanesInput == "" {
		c.displayPanesDeadline = time.Now().Add(s.sess.opts().paneNumberTimeoutDuration())
	} else {
		c.displayPanesDeadline = time.Now().Add(displayPanesDigitTimeout)
	}
	c.forceFull = true
	s.broadcast()
}

// paneNumberHasPrefix reports whether any pane number starts with prefix.
func paneNumberHasPrefix(prefix string, count int) bool {
	if prefix == "" || count <= 0 {
		return false
	}
	for i := 0; i < count; i++ {
		if strings.HasPrefix(strconv.Itoa(i), prefix) {
			return true
		}
	}
	return false
}

// paneNumberHasLongerMatch reports whether a longer pane number also starts
// with prefix, making the current input ambiguous.
func paneNumberHasLongerMatch(prefix string, count int) bool {
	for i := 0; i < count; i++ {
		n := strconv.Itoa(i)
		if n != prefix && strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// selectDisplayPane focuses the numbered pane. Selecting a hidden pane from
// a zoomed window restores the split layout, like jumping to a pane in tmux.
func (s *Server) selectDisplayPane(c *clientConn, number int) {
	w := s.activeWindow()
	if w == nil {
		return
	}
	ids := s.sess.windowPaneIDs(w)
	if number < 0 || number >= len(ids) {
		c.enqueue(protocol.Notice{Text: "qmm: invalid pane number"})
		return
	}
	target := ids[number]
	if target == w.activePane {
		return
	}
	if w.zoomed {
		w.zoomed = false
	}
	w.activePane = target
	s.relayout()
	s.needFull = true
}

// drawDisplayPanes paints a large numeral in the center of every pane and a
// short hint in the status bar. The underlying pane output remains visible;
// only the badge cells are covered.
func (c *compositor) drawDisplayPanes(frame *protocol.Frame, setCell helpSetCell, cols, rows int, showClock bool) {
	if cols < minCols || rows < minRows {
		return
	}
	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0

	panes := make([]protocol.PaneRect, 0, len(frame.Panes))
	maxNumber := -1
	for _, p := range frame.Panes {
		if p.Zoomed || p.Number < 0 {
			continue
		}
		p = clipPaneNumberRect(p, cols, rows)
		if p.W <= 0 || p.H <= 0 {
			continue
		}
		panes = append(panes, p)
		maxNumber = max(maxNumber, p.Number)
	}
	for _, p := range panes {
		c.drawPaneNumber(setCell, p)
	}
	drawDisplayPanesHint(frame, setCell, cols, rows, maxNumber+1, c.displayPanesInput, showClock)
}

func clipPaneNumberRect(p protocol.PaneRect, cols, rows int) protocol.PaneRect {
	x0, y0 := max(0, p.X), max(0, p.Y)
	x1 := min(cols, p.X+p.W)
	y1 := min(rows-statusRows, p.Y+p.H)
	p.X, p.Y = x0, y0
	p.W, p.H = max(0, x1-x0), max(0, y1-y0)
	return p
}

func (c *compositor) drawPaneNumber(setCell helpSetCell, p protocol.PaneRect) {
	label := strconv.Itoa(p.Number)
	fg, bg := colPaneNumberFG, colPaneNumberBG
	if p.Active {
		fg, bg = colPaneNumberActiveFG, colPaneNumberActiveBG
	}
	if drawLargePaneNumber(setCell, p, label, fg, bg) {
		return
	}
	drawCompactPaneNumber(setCell, p, label, fg, bg)
}

func drawLargePaneNumber(setCell helpSetCell, p protocol.PaneRect, label string, fg, bg int32) bool {
	digits := len(label)
	innerW := digits*3 + digits - 1
	boxW, boxH := innerW+2, 7
	if p.W < boxW || p.H < boxH {
		return false
	}
	x0 := p.X + (p.W-boxW)/2
	y0 := p.Y + (p.H-boxH)/2
	for y := y0; y < y0+boxH; y++ {
		for x := x0; x < x0+boxW; x++ {
			setCell(x, y, ' ', fg, bg, 0)
		}
	}
	for i, ch := range label {
		glyph := paneNumberGlyphs[int(ch-'0')]
		gx := x0 + 1 + i*4
		for row, line := range glyph {
			for col, mark := range line {
				if mark == '#' {
					setCell(gx+col, y0+1+row, ch, fg, bg, protocol.StyleBold)
				}
			}
		}
	}
	return true
}

func drawCompactPaneNumber(setCell helpSetCell, p protocol.PaneRect, label string, fg, bg int32) {
	text := "[" + label + "]"
	boxW := min(p.W, len(text)+2)
	boxH := min(p.H, 3)
	if boxW <= 0 || boxH <= 0 {
		return
	}
	x0 := p.X + (p.W-boxW)/2
	y0 := p.Y + (p.H-boxH)/2
	for y := y0; y < y0+boxH; y++ {
		for x := x0; x < x0+boxW; x++ {
			setCell(x, y, ' ', fg, bg, 0)
		}
	}
	textW := min(boxW, len(text))
	tx := x0 + (boxW-textW)/2
	ty := y0 + boxH/2
	for i := 0; i < textW; i++ {
		setCell(tx+i, ty, rune(text[i]), fg, bg, protocol.StyleBold)
	}
}

func drawDisplayPanesHint(frame *protocol.Frame, setCell helpSetCell, cols, rows, count int, input string, showClock bool) {
	y := rows - 1
	start := 0
	for _, tab := range frame.Tabs {
		start = max(start, tab.End)
	}
	if start < cols {
		start++
	}
	end := cols - 1
	if showClock {
		end = cols - displayPanesClockWidth - 1
	}
	width := end - start
	if width <= 0 {
		return
	}
	for x := start; x < end; x++ {
		setCell(x, y, ' ', colPaneNumberHintFG, colPaneNumberHintBG, 0)
	}
	hint := ""
	switch {
	case input != "":
		hint = fmt.Sprintf(" pane %s_ | Enter selects ", input)
		if len(hint) > width {
			hint = fmt.Sprintf(" %s_: Enter ", input)
		}
	case count > 0:
		hint = fmt.Sprintf(" panes 0-%d | number selects, Esc cancels ", count-1)
		if len(hint) > width {
			hint = fmt.Sprintf(" 0-%d: jump | Esc cancels ", count-1)
		}
		if len(hint) > width {
			hint = fmt.Sprintf(" pane 0-%d ", count-1)
		}
	}
	putHelpText(setCell, start, y, width, fitHelpText(hint, width), colPaneNumberHintFG, colPaneNumberHintBG, protocol.StyleBold)
}
