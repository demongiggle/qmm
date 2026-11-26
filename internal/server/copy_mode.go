package server

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	colCopySelectionBG int32 = 24
	colCopySelectionFG int32 = 231
	colCopyCursorBG    int32 = 46
	colCopyCursorFG    int32 = 16
	colCopyStatusBG    int32 = 236
	colCopyStatusFG    int32 = 248
	colCopyStatusKey   int32 = 46

	// OSC 52 payloads are capped so a huge selection cannot flood the
	// client's terminal with an enormous escape sequence.
	clipboardLimit = 64 * 1024
	wheelStepLines = 3
)

type selectionKind uint8

const (
	selectionNone selectionKind = iota
	selectionChar
	selectionLine
	selectionRect
)

type copyPoint struct {
	line int
	col  int
}

// copyModeState is one attached client's view of a pane's scrollback. It is
// deliberately per-client (like help and settings): the pane keeps running
// for everyone else while this client browses and selects text.
type copyModeState struct {
	open   bool
	paneID int
	lines  []copyLine
	cols   int // host screen size, used for the overlay viewport
	rows   int

	top    int // first buffer line shown
	cursor copyPoint

	selKind selectionKind
	anchor  copyPoint

	count    int  // pending numeric prefix for a motion
	pendingG bool // awaiting the second 'g' of gg

	searchActive   bool
	searchBack     bool
	query          string
	lastQuery      string
	lastSearchBack bool
}

func (st *copyModeState) viewRows() int {
	if st.rows < 2 {
		return 1
	}
	return st.rows - 1
}

func (st *copyModeState) maxTop() int {
	n := len(st.lines) - st.viewRows()
	if n < 0 {
		return 0
	}
	return n
}

func (st *copyModeState) clampTop() {
	st.top = clampInt(st.top, 0, st.maxTop())
}

func (st *copyModeState) lineLen(line int) int {
	if line < 0 || line >= len(st.lines) {
		return 1
	}
	n := len(st.lines[line].cells)
	if n < 1 {
		return 1
	}
	return n
}

func (st *copyModeState) clampCursor() {
	if len(st.lines) == 0 {
		st.cursor = copyPoint{}
		return
	}
	st.cursor.line = clampInt(st.cursor.line, 0, len(st.lines)-1)
	st.cursor.col = clampInt(st.cursor.col, 0, st.lineLen(st.cursor.line)-1)
}

func (st *copyModeState) ensureVisible() {
	st.clampCursor()
	if st.cursor.line < st.top {
		st.top = st.cursor.line
	} else if st.cursor.line >= st.top+st.viewRows() {
		st.top = st.cursor.line - st.viewRows() + 1
	}
	st.clampTop()
}

func (st *copyModeState) takeCount(def int) int {
	if st.count < 1 {
		return def
	}
	n := st.count
	st.count = 0
	return n
}

func (st *copyModeState) clearCount() {
	st.count = 0
	st.pendingG = false
}

// scrollBy moves the viewport and the cursor together. Positive n goes
// towards newer lines, negative n towards older scrollback.
func (st *copyModeState) scrollBy(n int) {
	old := st.top
	st.top = clampInt(st.top+n, 0, st.maxTop())
	st.cursor.line = clampInt(st.cursor.line+(st.top-old), 0, len(st.lines)-1)
	st.clampCursor()
	st.ensureVisible()
}

func (st *copyModeState) move(dLine, dCol, count int) {
	st.cursor.line = clampInt(st.cursor.line+dLine*count, 0, len(st.lines)-1)
	st.cursor.col = clampInt(st.cursor.col+dCol*count, 0, st.lineLen(st.cursor.line)-1)
	st.ensureVisible()
}

func (st *copyModeState) gotoLine(n int) {
	st.cursor.line = clampInt(n, 0, len(st.lines)-1)
	st.cursor.col = 0
	st.ensureVisible()
}

func (st *copyModeState) lineEnd() int {
	line := st.lines[st.cursor.line]
	for i := len(line.cells) - 1; i >= 0; i-- {
		if line.cells[i].Ch != ' ' && line.cells[i].Ch != 0 {
			return i
		}
	}
	return 0
}

func (st *copyModeState) firstNonBlank() int {
	line := st.lines[st.cursor.line]
	for i, c := range line.cells {
		if c.Ch != ' ' && c.Ch != 0 {
			return i
		}
	}
	return 0
}

func (st *copyModeState) cellAt(p copyPoint) rune {
	if p.line < 0 || p.line >= len(st.lines) {
		return ' '
	}
	line := st.lines[p.line]
	if p.col < 0 || p.col >= len(line.cells) {
		return ' '
	}
	if line.cells[p.col].Ch == 0 {
		return ' '
	}
	return line.cells[p.col].Ch
}

func charClass(ch rune) int {
	switch {
	case unicode.IsSpace(ch):
		return 0
	case unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_':
		return 1
	default:
		return 2
	}
}

func (st *copyModeState) nextPos(p copyPoint) (copyPoint, bool) {
	if p.col+1 < st.lineLen(p.line) {
		return copyPoint{p.line, p.col + 1}, true
	}
	if p.line+1 < len(st.lines) {
		return copyPoint{p.line + 1, 0}, true
	}
	return p, false
}

func (st *copyModeState) prevPos(p copyPoint) (copyPoint, bool) {
	if p.col > 0 {
		return copyPoint{p.line, p.col - 1}, true
	}
	if p.line > 0 {
		return copyPoint{p.line - 1, st.lineLen(p.line-1) - 1}, true
	}
	return p, false
}

func (st *copyModeState) wordForward(count int) {
	p := st.cursor
	for i := 0; i < count; i++ {
		cls := charClass(st.cellAt(p))
		q, ok := st.nextPos(p)
		moved := false
		for ok {
			next := charClass(st.cellAt(q))
			if q.line != p.line || next != cls {
				if next != 0 || q.line != p.line {
					p = q
					moved = true
					break
				}
				cls = 0
			}
			p = q
			q, ok = st.nextPos(p)
		}
		if !moved {
			break
		}
	}
	st.cursor = p
	st.ensureVisible()
}

func (st *copyModeState) wordBackward(count int) {
	p := st.cursor
	for i := 0; i < count; i++ {
		q, ok := st.prevPos(p)
		for ok && charClass(st.cellAt(q)) == 0 {
			q, ok = st.prevPos(q)
		}
		if !ok {
			p = q
			break
		}
		cls := charClass(st.cellAt(q))
		p = q
		for {
			q, ok = st.prevPos(p)
			if !ok || q.line != p.line || charClass(st.cellAt(q)) != cls {
				break
			}
			p = q
		}
	}
	st.cursor = p
	st.ensureVisible()
}

func (st *copyModeState) wordEnd(count int) {
	p := st.cursor
	for i := 0; i < count; i++ {
		q, ok := st.nextPos(p)
		for ok && charClass(st.cellAt(q)) == 0 {
			q, ok = st.nextPos(q)
		}
		if !ok {
			break
		}
		cls := charClass(st.cellAt(q))
		p = q
		for {
			q, ok = st.nextPos(p)
			if !ok || q.line != p.line || charClass(st.cellAt(q)) != cls {
				break
			}
			p = q
		}
	}
	st.cursor = p
	st.ensureVisible()
}

func (st *copyModeState) startSelection(kind selectionKind) {
	st.selKind = kind
	st.anchor = st.cursor
}

// selectionBounds returns the ordered anchor/cursor pair.
func (st *copyModeState) selectionBounds() (copyPoint, copyPoint) {
	a, b := st.anchor, st.cursor
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		return b, a
	}
	return a, b
}

func (st *copyModeState) inSelection(line, col int) bool {
	if st.selKind == selectionNone {
		return false
	}
	a, b := st.selectionBounds()
	if line < a.line || line > b.line {
		return false
	}
	switch st.selKind {
	case selectionLine:
		return true
	case selectionRect:
		c0, c1 := st.anchor.col, st.cursor.col
		if c1 < c0 {
			c0, c1 = c1, c0
		}
		return col >= c0 && col <= c1
	default:
		if a.line == b.line {
			return col >= a.col && col <= b.col
		}
		if line == a.line {
			return col >= a.col
		}
		if line == b.line {
			return col <= b.col
		}
		return true
	}
}

func (st *copyModeState) selectedText() string {
	if len(st.lines) == 0 {
		return ""
	}
	if st.selKind == selectionNone {
		return st.lines[clampInt(st.cursor.line, 0, len(st.lines)-1)].text()
	}
	a, b := st.selectionBounds()
	if st.selKind == selectionLine {
		parts := make([]string, 0, b.line-a.line+1)
		for line := a.line; line <= b.line; line++ {
			parts = append(parts, st.lines[line].text())
		}
		return strings.Join(parts, "\n")
	}
	var parts []string
	for line := a.line; line <= b.line; line++ {
		cells := st.lines[line].cells
		lo, hi := 0, len(cells)-1
		switch st.selKind {
		case selectionChar:
			if line == a.line {
				lo = a.col
			}
			if line == b.line {
				hi = b.col
			}
		case selectionRect:
			c0, c1 := st.anchor.col, st.cursor.col
			if c1 < c0 {
				c0, c1 = c1, c0
			}
			lo, hi = c0, c1
		}
		lo = clampInt(lo, 0, len(cells))
		hi = clampInt(hi, lo-1, len(cells)-1)
		if hi < lo {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, copyLine{cells: cells[lo : hi+1]}.text())
	}
	return strings.Join(parts, "\n")
}

func (st *copyModeState) findNext(reverse bool) {
	if st.lastQuery == "" || len(st.lines) == 0 {
		return
	}
	needle := strings.ToLower(st.lastQuery)
	dir := 1
	if st.lastSearchBack != reverse {
		dir = -1
	}
	n := len(st.lines)
	for step := 1; step <= n; step++ {
		idx := (st.cursor.line + dir*step + n*2) % n
		text := strings.ToLower(st.lines[idx].text())
		var at int
		if dir > 0 {
			at = strings.Index(text, needle)
		} else {
			at = strings.LastIndex(text, needle)
		}
		if at < 0 {
			continue
		}
		col := utf8.RuneCountInString(text[:at])
		st.cursor = copyPoint{line: idx, col: clampInt(col, 0, st.lineLen(idx)-1)}
		st.ensureVisible()
		return
	}
}

// ---------------------------------------------------------------------------
// open/close and input
// ---------------------------------------------------------------------------

func (s *Server) openCopyMode(c *clientConn, scrollUp int) {
	if c.copyMode != nil && c.copyMode.open {
		if scrollUp > 0 {
			c.copyMode.scrollBy(-scrollUp)
			c.forceFull = true
			s.broadcast()
		}
		return
	}
	p := s.sess.activePane()
	if p == nil {
		return
	}
	lines := p.copyBuffer()
	if len(lines) == 0 {
		lines = []copyLine{blankCopyLine(s.cols)}
	}
	st := &copyModeState{
		open:   true,
		paneID: p.id,
		lines:  lines,
		cols:   s.cols,
		rows:   s.rows,
	}
	st.cursor.line = len(lines) - 1
	st.top = st.maxTop()
	st.clampCursor()
	// Copy mode is exclusive: close any other local overlay first.
	c.helpOpen = false
	c.settingsOpen = false
	c.displayPanesOpen = false
	c.confirmOpen = false
	c.copyMode = st
	c.forceFull = true
	c.clearRepeat()
	if scrollUp > 0 {
		st.scrollBy(-scrollUp)
	}
	s.broadcast()
}

func (s *Server) closeCopyMode(c *clientConn) {
	if c.copyMode == nil || !c.copyMode.open {
		return
	}
	c.copyMode.open = false
	c.copyMode = nil
	c.forceFull = true
	c.clearRepeat()
	s.broadcast()
}

func (s *Server) resizeCopyModes() {
	for c := range s.clients {
		st := c.copyMode
		if st == nil || !st.open {
			continue
		}
		st.cols, st.rows = s.cols, s.rows
		st.ensureVisible()
		c.forceFull = true
	}
}

// handleWheel maps mouse wheel steps to copy-mode scrolling. Wheel-up enters
// copy mode (tmux behavior); wheel-down at the bottom of the buffer closes it.
func (s *Server) handleWheel(c *clientConn, delta int) {
	if delta == 0 {
		return
	}
	st := c.copyMode
	if st == nil || !st.open {
		if delta < 0 {
			s.openCopyMode(c, clampInt(-delta, 0, 20)*wheelStepLines)
		}
		return
	}
	if delta < 0 {
		st.scrollBy(clampInt(-delta, 0, 20) * wheelStepLines)
	} else {
		before := st.top
		st.scrollBy(clampInt(delta, 0, 20) * wheelStepLines)
		if st.top == before && st.top >= st.maxTop() {
			s.closeCopyMode(c)
			return
		}
	}
	c.forceFull = true
	s.broadcast()
}

func (s *Server) copySelection(c *clientConn) {
	st := c.copyMode
	if st == nil || !st.open {
		return
	}
	text := st.selectedText()
	if len(text) > clipboardLimit {
		text = text[:clipboardLimit]
	}
	if text != "" {
		s.pasteBuffer = text
		c.enqueue(protocol.Clipboard{Text: text})
	}
	s.closeCopyMode(c)
}

func (s *Server) pasteClipboard(c *clientConn) {
	if s.pasteBuffer == "" {
		c.enqueue(protocol.Notice{Text: "qmm: paste buffer is empty"})
		return
	}
	s.writeRaw(c, []byte(s.pasteBuffer))
}

// normalizeCopyKey folds xterm modifier variants of the arrow/Home/End keys
// back onto their plain sequences so copy mode sees one key per motion.
func normalizeCopyKey(seq string) string {
	if !strings.HasPrefix(seq, "\x1b[") || len(seq) < 4 {
		return seq
	}
	final := seq[len(seq)-1]
	switch final {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		return "\x1b[" + string(final)
	default:
		return seq
	}
}

// handleCopyModeInput consumes vim-style keys. Unknown keys are swallowed so
// nothing can leak into the pane while the overlay is open.
func (s *Server) handleCopyModeInput(c *clientConn, data []byte) {
	st := c.copyMode
	if st == nil || !st.open {
		return
	}
	seq := normalizeCopyKey(string(data))

	if st.searchActive {
		switch {
		case seq == "\r" || seq == "\n":
			st.lastQuery = st.query
			st.lastSearchBack = st.searchBack
			st.searchActive = false
			st.query = ""
			st.findNext(false)
		case seq == "\x1b" || seq == "\x03":
			st.searchActive = false
			st.query = ""
		case seq == "\x7f" || seq == "\x08":
			if st.query != "" {
				_, size := utf8.DecodeLastRuneInString(st.query)
				st.query = st.query[:len(st.query)-size]
			}
		default:
			if !strings.HasPrefix(seq, "\x1b") { // ignore arrow/function keys
				for _, ch := range seq {
					if ch >= 0x20 && ch != 0x7f {
						st.query += string(ch)
					}
				}
			}
		}
		c.forceFull = true
		s.broadcast()
		return
	}

	// A pending 'g' only means something when followed by another 'g'.
	if st.pendingG {
		st.pendingG = false
		if seq == "g" {
			n := st.takeCount(1)
			st.gotoLine(n - 1)
			c.forceFull = true
			s.broadcast()
		} else {
			st.clearCount()
		}
		return
	}

	if len(seq) == 1 && seq[0] >= '0' && seq[0] <= '9' && (st.count > 0 || seq[0] != '0') {
		st.count = clampInt(st.count*10+int(seq[0]-'0'), 0, 100000)
		return
	}

	up := seq == "k" || seq == "\x1b[A" || seq == "\x1bOA"
	down := seq == "j" || seq == "\x1b[B" || seq == "\x1bOB"
	left := seq == "h" || seq == "\x1b[D" || seq == "\x1bOD"
	right := seq == "l" || seq == "\x1b[C" || seq == "\x1bOC"

	switch {
	case up:
		st.move(-1, 0, st.takeCount(1))
	case down:
		st.move(1, 0, st.takeCount(1))
	case left:
		st.move(0, -1, st.takeCount(1))
	case right:
		st.move(0, 1, st.takeCount(1))
	case seq == "w":
		st.wordForward(st.takeCount(1))
	case seq == "b":
		st.wordBackward(st.takeCount(1))
	case seq == "e":
		st.wordEnd(st.takeCount(1))
	case seq == "0":
		st.cursor.col = 0
		st.ensureVisible()
	case seq == "^":
		st.cursor.col = st.firstNonBlank()
		st.ensureVisible()
	case seq == "$":
		st.cursor.col = st.lineEnd()
		st.ensureVisible()
	case seq == "g":
		st.pendingG = true
	case seq == "G":
		n := st.takeCount(0)
		if n == 0 {
			st.gotoLine(len(st.lines) - 1)
		} else {
			st.gotoLine(n - 1)
		}
	case seq == "H":
		st.gotoLine(st.top)
	case seq == "M":
		st.gotoLine(st.top + st.viewRows()/2)
	case seq == "L":
		st.gotoLine(st.top + st.viewRows() - 1)
	case seq == "\x15" || seq == "\x02" || seq == "\x1b[5~": // Ctrl-u / Ctrl-b / PgUp
		n := st.viewRows() / 2
		if seq == "\x02" || seq == "\x1b[5~" {
			n = st.viewRows()
		}
		if st.count > 0 {
			n = st.takeCount(n)
		} else {
			st.clearCount()
		}
		st.scrollBy(-n)
	case seq == "\x04" || seq == "\x06" || seq == "\x1b[6~": // Ctrl-d / Ctrl-f / PgDn
		n := st.viewRows() / 2
		if seq == "\x06" || seq == "\x1b[6~" {
			n = st.viewRows()
		}
		if st.count > 0 {
			n = st.takeCount(n)
		} else {
			st.clearCount()
		}
		st.scrollBy(n)
	case seq == "\x1b[H" || seq == "\x1bOH":
		st.cursor.col = 0
		st.ensureVisible()
	case seq == "\x1b[F" || seq == "\x1bOF":
		st.cursor.col = st.lineEnd()
		st.ensureVisible()
	case seq == "v":
		st.startSelection(selectionChar)
	case seq == "V":
		st.startSelection(selectionLine)
	case seq == "\x16": // Ctrl-v
		st.startSelection(selectionRect)
	case seq == "/":
		st.searchActive, st.searchBack, st.query = true, false, ""
	case seq == "?":
		st.searchActive, st.searchBack, st.query = true, true, ""
	case seq == "n":
		st.findNext(false)
	case seq == "N":
		st.findNext(true)
	case seq == "y" || seq == "\r" || seq == "\n":
		s.copySelection(c)
		return
	case seq == "q" || seq == "\x1b" || seq == "\x03":
		s.closeCopyMode(c)
		return
	default:
		st.clearCount()
	}
	c.forceFull = true
	s.broadcast()
}

// ---------------------------------------------------------------------------
// overlay rendering
// ---------------------------------------------------------------------------

func (c *compositor) drawCopyMode(frame *protocol.Frame, setCell helpSetCell, cols, rows int, st *copyModeState) {
	frame.Panes = nil
	frame.Splitters = nil
	frame.Tabs = nil
	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0

	viewRows := rows - 1
	if viewRows < 1 {
		viewRows = 1
	}
	for y := 0; y < viewRows && y < rows; y++ {
		idx := st.top + y
		skip := 0
		for x := 0; x < cols; x++ {
			cell := blankCopyCell()
			if idx >= 0 && idx < len(st.lines) && x < len(st.lines[idx].cells) {
				cell = st.lines[idx].cells[x]
			}
			ch := cell.Ch
			if ch == 0 {
				ch = ' '
			}
			if skip > 0 {
				ch = ' '
				skip--
			}
			fg, bg, style := cell.FG, cell.BG, cell.Style
			if st.inSelection(idx, x) {
				fg, bg, style = colCopySelectionFG, colCopySelectionBG, 0
			}
			if idx == st.cursor.line && x == st.cursor.col {
				fg, bg, style = colCopyCursorFG, colCopyCursorBG, protocol.StyleBold
			}
			if skip == 0 && uniseg.StringWidth(string(ch)) == 2 {
				skip = 1
			}
			setCell(x, y, ch, fg, bg, style)
		}
	}

	statusY := rows - 1
	if statusY < 0 {
		return
	}
	for x := 0; x < cols; x++ {
		setCell(x, statusY, ' ', colCopyStatusFG, colCopyStatusBG, 0)
	}
	var label string
	switch {
	case st.searchActive:
		prefix := "/"
		if st.searchBack {
			prefix = "?"
		}
		label = fmt.Sprintf(" %s%s_   [Enter] find  [Esc] cancel", prefix, st.query)
	case st.selKind == selectionChar:
		label = fmt.Sprintf(" -- VISUAL --  line %d/%d  col %d", st.cursor.line+1, len(st.lines), st.cursor.col+1)
	case st.selKind == selectionLine:
		label = fmt.Sprintf(" -- VISUAL LINE --  line %d/%d", st.cursor.line+1, len(st.lines))
	case st.selKind == selectionRect:
		label = fmt.Sprintf(" -- VISUAL BLOCK --  line %d/%d  col %d", st.cursor.line+1, len(st.lines), st.cursor.col+1)
	default:
		label = fmt.Sprintf(" [copy mode]  line %d/%d  col %d", st.cursor.line+1, len(st.lines), st.cursor.col+1)
	}
	hint := "  q quit  y copy  v select  / search"
	if len([]rune(label))+len([]rune(hint)) > cols {
		hint = ""
	}
	runes := []rune(label + hint)
	for i, ch := range runes {
		if i >= cols {
			break
		}
		setCell(i, statusY, ch, colCopyStatusKey, colCopyStatusBG, 0)
	}
}
