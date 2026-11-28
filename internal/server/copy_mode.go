package server

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	colCopySelectionBG int32 = 24
	colCopySelectionFG int32 = 231
	colCopyCursorBG    int32 = 46
	colCopyCursorFG    int32 = 16
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
// for everyone else while this client browses and selects text. Mouse drags
// do not open copy mode; they use the non-modal selectionState instead.
type copyModeState struct {
	open   bool
	paneID int
	// rect is the pane's screen viewport. Copy mode draws inside it and leaves
	// the rest of the layout (other panes, borders, status bar) live, so it
	// follows the layout while it stays open on the same pane.
	rect layout.Rect
	// mouseDrag is true once a drag inside the copy view has re-anchored the
	// selection, so select-update messages extend it instead of moving the
	// keyboard cursor.
	mouseDrag bool
	lines     []copyLine

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

// viewRows is how many scrollback lines the pane shows. The copy status line
// covers the session status bar instead of the pane's last row, so the view
// keeps the pane's full height.
func (st *copyModeState) viewRows() int {
	if st.rect.H < 1 {
		return 1
	}
	return st.rect.H
}

// covers reports whether the copy view is drawn at (x, y).
func (st *copyModeState) covers(x, y int) bool {
	return st.rect.Contains(x, y)
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
	return lineLenAt(st.lines, line)
}

// lineLenAt returns the cell count of a snapshot line, treating empty or
// out-of-range lines as a single blank cell.
func lineLenAt(lines []copyLine, line int) int {
	if line < 0 || line >= len(lines) {
		return 1
	}
	if n := len(lines[line].cells); n > 0 {
		return n
	}
	return 1
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
	return orderedPoints(st.anchor, st.cursor)
}

// orderedPoints returns the two selection endpoints in reading order.
func orderedPoints(a, b copyPoint) (copyPoint, copyPoint) {
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		return b, a
	}
	return a, b
}

func (st *copyModeState) inSelection(line, col int) bool {
	return pointSelected(st.selKind, st.anchor, st.cursor, line, col)
}

// pointSelected reports whether (line, col) falls inside the selection that
// spans anchor..cursor. It is shared by copy mode and mouse selections.
func pointSelected(kind selectionKind, anchor, cursor copyPoint, line, col int) bool {
	if kind == selectionNone {
		return false
	}
	a, b := orderedPoints(anchor, cursor)
	if line < a.line || line > b.line {
		return false
	}
	switch kind {
	case selectionLine:
		return true
	case selectionRect:
		c0, c1 := anchor.col, cursor.col
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
	return selectionText(st.lines, st.selKind, st.anchor, st.cursor)
}

// selectionText extracts the text covered by the selection. Line-wise and
// rectangle selections extend to line ends and column bounds respectively.
func selectionText(lines []copyLine, kind selectionKind, anchor, cursor copyPoint) string {
	if len(lines) == 0 || kind == selectionNone {
		return ""
	}
	a, b := orderedPoints(anchor, cursor)
	a.line = clampInt(a.line, 0, len(lines)-1)
	b.line = clampInt(b.line, 0, len(lines)-1)
	if kind == selectionLine {
		parts := make([]string, 0, b.line-a.line+1)
		for line := a.line; line <= b.line; line++ {
			parts = append(parts, lines[line].text())
		}
		return strings.Join(parts, "\n")
	}
	var parts []string
	for line := a.line; line <= b.line; line++ {
		cells := lines[line].cells
		lo, hi := 0, len(cells)-1
		switch kind {
		case selectionChar:
			if line == a.line {
				lo = a.col
			}
			if line == b.line {
				hi = b.col
			}
		case selectionRect:
			c0, c1 := anchor.col, cursor.col
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
		// The match offset is a rune index in the copy text, but cursor
		// columns are terminal columns; placeholders widen lines past the
		// runes text() returns, so convert before placing the cursor.
		col := st.lines[idx].colForRune(utf8.RuneCountInString(text[:at]))
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
	rect, ok := s.paneRects()[p.id]
	if !ok || rect.W < 1 || rect.H < 1 {
		return
	}
	lines, cursor := p.copyBufferAtCursor()
	if len(lines) == 0 {
		lines = []copyLine{blankCopyLine(rect.W)}
		cursor = copyPoint{}
	}
	st := &copyModeState{
		open:   true,
		paneID: p.id,
		lines:  lines,
		rect:   rect,
		cursor: cursor,
	}
	// Start where the pane's cursor is (tmux behavior), with the bottom of the
	// buffer in view so the live screen maps onto the pane as the user left it.
	st.top = st.maxTop()
	st.ensureVisible()
	// Copy mode is exclusive: close any other local overlay first.
	c.helpOpen = false
	c.settingsOpen = false
	c.displayPanesOpen = false
	c.confirmOpen = false
	c.resetLayoutsMenu()
	c.copyMode = st
	c.forceFull = true
	c.clearRepeat()
	if scrollUp > 0 {
		st.scrollBy(-scrollUp)
	}
	s.broadcast()
}

// copyModeConsumesAction lets selection and wheel events reach the copy
// handlers. A gesture outside the pane closes its copy view before normal
// dispatch continues.
func (s *Server) copyModeConsumesAction(c *clientConn, action any) bool {
	switch msg := action.(type) {
	case protocol.ActionWheel, protocol.ActionSelectUpdate:
	case protocol.ActionSelectStart:
		if !c.copyMode.covers(msg.X, msg.Y) {
			s.closeCopyMode(c)
		}
	case protocol.ActionFocus:
		if !c.copyMode.covers(msg.X, msg.Y) {
			s.closeCopyMode(c)
		}
	case protocol.ActionSelectWindow, protocol.ActionDragStart, protocol.ActionTogglePaneZoom:
		s.closeCopyMode(c)
	default:
		return true
	}
	return false
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

// scrToBuffer maps screen cell coordinates onto the copy-mode buffer. The copy
// view fills the pane rect line for line. Columns are clamped to the line
// length; points outside the pane clamp to its edges so a drag that leaves the
// pane keeps extending the selection.
func (st *copyModeState) scrToBuffer(x, y int) copyPoint {
	viewRows := st.viewRows()
	row := clampInt(y-st.rect.Y, 0, viewRows-1)
	line := st.top + row
	if len(st.lines) == 0 {
		line = 0
	} else {
		line = clampInt(line, 0, len(st.lines)-1)
	}
	maxCol := st.lineLen(line) - 1
	if maxCol < 0 {
		maxCol = 0
	}
	return copyPoint{line: line, col: clampInt(x-st.rect.X, 0, maxCol)}
}

// syncCopyModes keeps every client's copy view pinned to the pane it was
// opened on. It runs at the start of broadcast, so a layout change is reflected
// before the frame is composed: the view follows its pane, and a pane that left
// the active window (window switch, pane exit, zoom of another pane) closes the
// view instead of leaving it drawn over the wrong cells.
func (s *Server) syncCopyModes() {
	if s.sess == nil {
		return
	}
	var rects map[int]layout.Rect
	for c := range s.clients {
		st := c.copyMode
		if st == nil || !st.open {
			continue
		}
		if rects == nil {
			rects = s.paneRects()
		}
		r, ok := rects[st.paneID]
		if !ok || r.W < 1 || r.H < 1 || s.sess.panes[st.paneID] == nil {
			st.open = false
			c.copyMode = nil
			c.clearRepeat()
			c.forceFull = true
			continue
		}
		if r != st.rect {
			st.rect = r
			st.ensureVisible()
			c.forceFull = true
		}
	}
}

// handleWheel maps mouse wheel steps to copy-mode scrolling. Wheel-up enters
// copy mode (tmux behavior); wheel-down at the bottom of the buffer closes it.
func (s *Server) handleWheel(c *clientConn, delta int) {
	if delta == 0 {
		return
	}
	// A pending mouse selection owns the wheel: it extends the selection into
	// the frozen snapshot instead of scrolling the live pane.
	if c.selection != nil {
		s.wheelSelection(c, delta)
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
		// Towards older scrollback lines.
		st.scrollBy(-clampInt(-delta, 0, 20) * wheelStepLines)
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
	s.deliverClipboard(c, st.selectedText())
	s.closeCopyMode(c)
}

// deliverClipboard hands text to the host clipboard (OSC 52) and to qmm's
// paste buffer. Empty text is dropped so a stray copy key does not wipe the
// buffer.
func (s *Server) deliverClipboard(c *clientConn, text string) {
	text = clampClipboardText(text, clipboardLimit)
	if text == "" {
		return
	}
	s.pasteBuffer = text
	c.enqueue(protocol.Clipboard{Text: text})
}

// clampClipboardText caps a selection at limit bytes without cutting a
// multi-byte UTF-8 rune in half. The result is always valid UTF-8 for the
// rune-based text produced by copyLine.text().
func clampClipboardText(text string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
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

	// The configured copy key behaves like y: copy the selection and leave
	// copy mode. y and Enter remain available as built-in fallbacks.
	if s.sess != nil && seq == copyKeySequence(s.sess.opts()) {
		s.copySelection(c)
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
	case seq == "q" || seq == "\x1b":
		s.closeCopyMode(c)
		return
	case seq == "\x03": // Ctrl-c
		// Plain Ctrl-C reaches the server even when the terminal grabs the
		// configured modified copy chord for its own clipboard. Copy when a
		// selection is active (mouse drag, v/V/Ctrl-v); otherwise leave
		// without copying so an empty copy mode still quits cleanly.
		if st.selKind != selectionNone {
			s.copySelection(c)
			return
		}
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

// blitSnapshotLine writes one scrollback line into a screen row starting at
// column x0. Scrollback lines live in terminal columns: a double-width rune
// owns two cells and a zero-rune placeholder marks its blank second half, so
// buffer columns map 1:1 onto screen columns and the pane caret, mouse
// coordinates, and selection columns all agree. hl may be nil; when it reports
// a hit for a column, the cell is drawn with the returned style (copy mode
// uses this for the selection and cursor).
func blitSnapshotLine(setCell helpSetCell, line copyLine, x0, y, width int, hl func(col int) (fg, bg int32, style uint8, ok bool), mapColor func(int32, bool) int32) {
	if mapColor == nil {
		mapColor = func(c int32, _ bool) int32 { return c }
	}
	for x := 0; x < width; x++ {
		cell := blankCopyCell()
		if x < len(line.cells) {
			cell = line.cells[x]
		}
		ch := cell.Ch
		if ch == 0 {
			ch = ' '
		}
		fg, bg, style := mapColor(cell.FG, true), mapColor(cell.BG, false), cell.Style
		if hl != nil {
			if hfg, hbg, hstyle, ok := hl(x); ok {
				fg, bg, style = hfg, hbg, hstyle
			}
		}
		setCell(x0+x, y, ch, fg, bg, style)
	}
}

// copyModeCovers reports whether this client's open copy view replaces the
// given pane's cells, so the pane's live caret must stay hidden.
func (c *compositor) copyModeCovers(paneID int) bool {
	return c.copyMode != nil && c.copyMode.open && c.copyMode.paneID == paneID
}

// drawCopyMode paints the copy view into its pane's rect. Everything outside
// the rect (other panes, borders, the bar row) stays as composed, so the
// session layout remains visible while one pane shows its scrollback.
func (c *compositor) drawCopyMode(setCell helpSetCell, st *copyModeState) {
	r := st.rect
	if r.W < 1 || r.H < 1 {
		return
	}
	for y := 0; y < st.viewRows(); y++ {
		idx := st.top + y
		line := copyLine{}
		if idx >= 0 && idx < len(st.lines) {
			line = st.lines[idx]
		}
		blitSnapshotLine(setCell, line, r.X, r.Y+y, r.W, func(col int) (int32, int32, uint8, bool) {
			if st.inSelection(idx, col) {
				return c.chrome.copySelFG, c.chrome.copySelBG, 0, true
			}
			if idx == st.cursor.line && col == st.cursor.col {
				return c.chrome.copyCursorFG, c.chrome.copyCursorBG, protocol.StyleBold, true
			}
			return 0, 0, 0, false
		}, c.chrome.mapColor)
	}
}

// drawCopyStatusBar paints the copy-mode status line over the session status
// bar. Covering the bar keeps the pane's last row for scrollback and makes the
// copy state obvious: the line uses its own configurable palette instead of
// the session tabs and clock.
func (c *compositor) drawCopyStatusBar(setCell helpSetCell, opts *settingsState, cols, rows int, st *copyModeState) {
	if cols < 1 || rows < 1 {
		return
	}
	style := opts.copyStatusStyle()
	y := rows - 1
	for x := 0; x < cols; x++ {
		setCell(x, y, ' ', style.fg, style.bg, 0)
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
	x := 0
	put := func(text string, fg int32) {
		for _, ch := range text {
			if x >= cols {
				return
			}
			setCell(x, y, ch, fg, style.bg, 0)
			x++
		}
	}
	put(label, style.accent)
	put(hint, style.fg)
}
