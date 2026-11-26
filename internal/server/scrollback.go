package server

import (
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

// scrollbackLimit is the number of scrolled-off lines kept per pane. Lines
// are trimmed of trailing blank cells before they enter the ring, so the
// memory cost is proportional to the text actually on screen.
const scrollbackLimit = 2000

// copyCell is one character in the copy-mode buffer. It deliberately mirrors
// protocol.Cell's visual fields without the screen coordinates.
type copyCell struct {
	Ch    rune
	FG    int32
	BG    int32
	Style uint8
}

type copyLine struct {
	cells []copyCell
}

func blankCopyCell() copyCell {
	return copyCell{Ch: ' ', FG: protocol.ColorDefault, BG: protocol.ColorDefault}
}

func blankCopyLine(cols int) copyLine {
	if cols < 1 {
		cols = 1
	}
	cells := make([]copyCell, cols)
	for i := range cells {
		cells[i] = blankCopyCell()
	}
	return copyLine{cells: cells}
}

// text returns the line's copyable text: placeholder cells for wide runes are
// skipped and trailing whitespace is removed, like tmux's copy-selection.
func (l copyLine) text() string {
	var sb strings.Builder
	last := -1
	for _, c := range l.cells {
		if c.Ch == 0 {
			continue
		}
		sb.WriteRune(c.Ch)
		if c.Ch != ' ' {
			last = sb.Len()
		}
	}
	if last < 0 {
		return ""
	}
	return sb.String()[:last]
}

func (l copyLine) clone() copyLine {
	cells := make([]copyCell, len(l.cells))
	copy(cells, l.cells)
	return copyLine{cells: cells}
}

// trimmedCopyLine removes trailing blank cells but keeps at least one cell so
// the line still has a defined width.
func trimmedCopyLine(l copyLine) copyLine {
	width := len(l.cells)
	for width > 1 {
		c := l.cells[width-1]
		if c.Ch != ' ' && c.Ch != 0 {
			break
		}
		width--
	}
	return copyLine{cells: append([]copyCell(nil), l.cells[:width]...)}
}

type scrollbackState uint8

const (
	sbGround scrollbackState = iota
	sbEsc
	sbCSI
	sbOSC
	sbOSCEsc
	sbString
	sbCharset
)

// scrollback is a small line-oriented terminal recorder. It receives the same
// raw PTY bytes as the pane's vt10x emulator through io.TeeReader and keeps
// the lines that scroll off the top of the screen. It is intentionally much
// simpler than a full terminal emulator: the visible screen at copy-mode entry
// always comes from vt10x itself, so the recorder only has to model the
// scrolling behavior of ordinary shell output.
//
// Alternate-screen programs (vim, less, top, etc.) are ignored while the
// alternate screen is active; their redraws are not useful scrollback.
type scrollback struct {
	mu    sync.Mutex
	limit int

	cols, rows int
	top        int // scrolling region, inclusive
	bottom     int

	x, y     int
	wrapNext bool
	savedX   int
	savedY   int
	attr     copyCell
	screen   []copyLine
	lines    []copyLine // ring of scrolled-off lines, oldest first
	alt      bool
	state    scrollbackState
	params   []byte
	carry    []byte // incomplete UTF-8 rune between Write calls
}

func newScrollback(cols, rows int) *scrollback {
	r := &scrollback{limit: scrollbackLimit, attr: blankCopyCell()}
	r.initScreen(cols, rows)
	return r
}

// initScreen resets the emulated screen. The caller must hold r.mu.
func (r *scrollback) initScreen(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	r.cols, r.rows = cols, rows
	r.top, r.bottom = 0, rows-1
	r.x, r.y = 0, 0
	r.wrapNext = false
	r.savedX, r.savedY = 0, 0
	r.screen = make([]copyLine, rows)
	for i := range r.screen {
		r.screen[i] = blankCopyLine(cols)
	}
}

// Write consumes raw PTY bytes. It implements io.Writer for io.TeeReader.
func (r *scrollback) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := append(r.carry, p...)
	r.carry = nil
	for len(buf) > 0 {
		if !utf8.FullRune(buf) {
			r.carry = append([]byte(nil), buf...)
			break
		}
		ch, size := utf8.DecodeRune(buf)
		buf = buf[size:]
		r.putRune(ch)
	}
	return len(p), nil
}

// Resize keeps the current screen text but changes its dimensions.
func (r *scrollback) Resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	screen := make([]copyLine, rows)
	for y := 0; y < rows; y++ {
		line := blankCopyLine(cols)
		if y < len(r.screen) {
			copy(line.cells, r.screen[y].cells)
		}
		screen[y] = line
	}
	r.cols, r.rows = cols, rows
	r.screen = screen
	r.top, r.bottom = 0, rows-1
	r.x = clampInt(r.x, 0, cols-1)
	r.y = clampInt(r.y, 0, rows-1)
	r.savedX = clampInt(r.savedX, 0, cols-1)
	r.savedY = clampInt(r.savedY, 0, rows-1)
	r.wrapNext = false
}

// SetScreen replaces the recorder's emulated screen with an exact snapshot of
// the pane's vt10x screen. readLoop calls it after every parsed chunk, so any
// divergence caused by an uncommon escape sequence is corrected before the
// next chunk rather than accumulating.
func (r *scrollback) SetScreen(lines []copyLine, curX, curY int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := len(lines)
	if rows < 1 {
		rows = 1
	}
	cols := r.cols
	if rows > 0 && len(lines[0].cells) > 0 {
		cols = len(lines[0].cells)
	}
	r.cols, r.rows = cols, rows
	r.screen = make([]copyLine, rows)
	for y := 0; y < rows; y++ {
		if y < len(lines) {
			cells := make([]copyCell, cols)
			copy(cells, lines[y].cells)
			r.screen[y] = copyLine{cells: cells}
		} else {
			r.screen[y] = blankCopyLine(cols)
		}
	}
	r.top = clampInt(r.top, 0, rows-1)
	r.bottom = clampInt(r.bottom, r.top, rows-1)
	r.x = clampInt(curX, 0, cols-1)
	r.y = clampInt(curY, 0, rows-1)
	r.savedX, r.savedY = r.x, r.y
	r.wrapNext = false
}

// History returns a snapshot of the scrolled-off lines, oldest first.
func (r *scrollback) History() []copyLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]copyLine, len(r.lines))
	for i, l := range r.lines {
		out[i] = l.clone()
	}
	return out
}

func (r *scrollback) pushHistory(l copyLine) {
	r.lines = append(r.lines, trimmedCopyLine(l))
	if len(r.lines) > r.limit {
		copy(r.lines, r.lines[len(r.lines)-r.limit:])
		r.lines = r.lines[:r.limit]
	}
}

func (r *scrollback) putRune(ch rune) {
	switch r.state {
	case sbGround:
		// Alternate-screen redraws are not scrollback: ignore everything
		// except the ESC that starts the sequence which may leave alt mode.
		if r.alt && ch != 0x1b {
			return
		}
		switch ch {
		case 0x1b:
			r.state = sbEsc
		case '\r':
			r.x = 0
			r.wrapNext = false
		case '\n', '\v', '\f':
			r.newline(false)
		case '\b':
			r.x = clampInt(r.x-1, 0, r.cols-1)
			r.wrapNext = false
		case '\t':
			r.x = (r.x/8 + 1) * 8
			if r.x >= r.cols {
				r.x = r.cols - 1
				r.wrapNext = true
			}
		case 0x07, 0x00:
			// BEL / NUL: no effect.
		default:
			if ch >= 0x20 && ch != 0x7f {
				r.put(ch)
			}
		}
	case sbEsc:
		r.handleEsc(ch)
	case sbCSI:
		if ch >= 0x40 && ch <= 0x7e {
			r.handleCSI(ch)
			r.params = nil
			r.state = sbGround
		} else {
			r.params = appendCSIByte(r.params, ch)
		}
	case sbOSC:
		if ch == 0x07 {
			r.state = sbGround
		} else if ch == 0x1b {
			r.state = sbOSCEsc
		}
	case sbOSCEsc:
		if ch == '\\' {
			r.state = sbGround
		} else {
			r.state = sbOSC
		}
	case sbString:
		if ch == 0x07 {
			r.state = sbGround
		} else if ch == 0x1b {
			r.state = sbOSCEsc
		}
	case sbCharset:
		r.state = sbGround
	}
}

func appendCSIByte(dst []byte, ch rune) []byte {
	if ch > 0xff {
		return dst
	}
	return append(dst, byte(ch))
}

func (r *scrollback) handleEsc(ch rune) {
	switch ch {
	case '[':
		r.state = sbCSI
		r.params = nil
	case ']':
		r.state = sbOSC
	case 'P', 'X', '^', '_':
		r.state = sbString
	case '(', ')', '*', '+':
		r.state = sbCharset
	case 'D': // IND: index, scroll at bottom margin
		if !r.alt {
			r.newline(false)
		}
		r.state = sbGround
	case 'E': // NEL: next line
		if !r.alt {
			r.newline(true)
		}
		r.state = sbGround
	case 'M': // RI: reverse index
		if !r.alt {
			r.reverseIndex()
		}
		r.state = sbGround
	case '7':
		r.savedX, r.savedY = r.x, r.y
		r.state = sbGround
	case '8':
		r.x, r.y = r.savedX, r.savedY
		r.wrapNext = false
		r.state = sbGround
	case 'c': // RIS
		if !r.alt {
			r.clearScreen(2)
			r.x, r.y = 0, 0
			r.attr = blankCopyCell()
		}
		r.state = sbGround
	default:
		r.state = sbGround
	}
}

func (r *scrollback) handleCSI(final rune) {
	raw := string(r.params)
	priv := strings.HasPrefix(raw, "?")
	if priv {
		raw = raw[1:]
	}
	params := parseCSIParams(raw)

	// Alternate-screen switches must be seen even while the alternate screen
	// is active: that is how we learn when to resume recording.
	if priv && (final == 'h' || final == 'l') {
		for _, p := range params {
			if p == 1049 || p == 47 || p == 1047 {
				if final == 'h' {
					r.alt = true
					r.x, r.y = 0, 0
					r.wrapNext = false
				} else {
					r.alt = false
					r.clearScreen(2)
					r.x, r.y = 0, 0
					r.wrapNext = false
				}
				return
			}
		}
	}
	if r.alt {
		return
	}

	n := func(i, def int) int {
		if i >= len(params) || params[i] == 0 {
			return def
		}
		return params[i]
	}
	switch final {
	case 'A':
		r.y = clampInt(r.y-n(0, 1), 0, r.rows-1)
		r.wrapNext = false
	case 'B':
		r.y = clampInt(r.y+n(0, 1), 0, r.rows-1)
		r.wrapNext = false
	case 'C':
		r.x = clampInt(r.x+n(0, 1), 0, r.cols-1)
		r.wrapNext = false
	case 'D':
		r.x = clampInt(r.x-n(0, 1), 0, r.cols-1)
		r.wrapNext = false
	case 'd':
		r.y = clampInt(n(0, 1)-1, 0, r.rows-1)
	case 'G':
		r.x = clampInt(n(0, 1)-1, 0, r.cols-1)
		r.wrapNext = false
	case 'H', 'f':
		r.y = clampInt(n(0, 1)-1, 0, r.rows-1)
		r.x = clampInt(n(1, 1)-1, 0, r.cols-1)
		r.wrapNext = false
	case 'J':
		r.clearScreen(n(0, 0))
	case 'K':
		r.eraseLine(n(0, 0))
	case 'S':
		r.scrollUp(n(0, 1))
	case 'T':
		r.scrollDown(n(0, 1))
	case 'L':
		r.insertLines(n(0, 1))
	case 'M':
		r.deleteLines(n(0, 1))
	case '@':
		r.insertChars(n(0, 1))
	case 'P':
		r.deleteChars(n(0, 1))
	case 'X':
		r.eraseChars(n(0, 1))
	case 'm':
		r.applySGR(params)
	case 'r':
		t := n(0, 1) - 1
		b := n(1, r.rows) - 1
		if t >= 0 && b < r.rows && t < b {
			r.top, r.bottom = t, b
		} else {
			r.top, r.bottom = 0, r.rows-1
		}
		r.x, r.y = 0, 0
		r.wrapNext = false
	}
}

func parseCSIParams(raw string) []int {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	params := make([]int, 0, len(parts))
	for _, part := range parts {
		// Ignore sub-parameters (e.g. 38:5:...) and intermediate bytes.
		if i := strings.IndexByte(part, ':'); i >= 0 {
			part = part[:i]
		}
		v := 0
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				v = 0
				break
			}
			v = v*10 + int(ch-'0')
			if v > 1<<20 {
				v = 1 << 20
			}
		}
		params = append(params, v)
	}
	return params
}

func (r *scrollback) applySGR(params []int) {
	if len(params) == 0 {
		r.attr = blankCopyCell()
		return
	}
	for i := 0; i < len(params); i++ {
		p := params[i]
		switch {
		case p == 0:
			r.attr = blankCopyCell()
		case p == 1:
			r.attr.Style |= protocol.StyleBold
		case p == 3:
			r.attr.Style |= protocol.StyleItalic
		case p == 4:
			r.attr.Style |= protocol.StyleUnderline
		case p == 5:
			r.attr.Style |= protocol.StyleBlink
		case p == 7:
			r.attr.Style |= protocol.StyleReverse
		case p == 22:
			r.attr.Style &^= protocol.StyleBold | protocol.StyleItalic
		case p == 24:
			r.attr.Style &^= protocol.StyleUnderline
		case p == 25:
			r.attr.Style &^= protocol.StyleBlink
		case p == 27:
			r.attr.Style &^= protocol.StyleReverse
		case p >= 30 && p <= 37:
			r.attr.FG = int32(p - 30)
		case p >= 40 && p <= 47:
			r.attr.BG = int32(p - 40)
		case p >= 90 && p <= 97:
			r.attr.FG = int32(p - 90 + 8)
		case p >= 100 && p <= 107:
			r.attr.BG = int32(p - 100 + 8)
		case p == 39:
			r.attr.FG = protocol.ColorDefault
		case p == 49:
			r.attr.BG = protocol.ColorDefault
		case p == 38 || p == 48:
			if i+1 >= len(params) {
				continue
			}
			mode := params[i+1]
			var color int32
			switch mode {
			case 5:
				if i+2 < len(params) {
					color = int32(clampInt(params[i+2], 0, 255))
					i += 2
				}
			case 2:
				if i+4 < len(params) {
					color = protocol.RGBColor(
						int32(clampInt(params[i+2], 0, 255)),
						int32(clampInt(params[i+3], 0, 255)),
						int32(clampInt(params[i+4], 0, 255)),
					)
					i += 4
				}
			default:
				continue
			}
			if p == 38 {
				r.attr.FG = color
			} else {
				r.attr.BG = color
			}
		}
	}
}

func (r *scrollback) put(ch rune) {
	if r.wrapNext {
		r.newline(true)
		r.wrapNext = false
	}
	width := uniseg.StringWidth(string(ch))
	if width < 1 {
		width = 1
	}
	line := r.screen[r.y]
	for i := 0; i < width && r.x+i < r.cols; i++ {
		cell := r.attr
		if i == 0 {
			cell.Ch = ch
		} else {
			cell.Ch = 0 // wide-rune placeholder
		}
		line.cells[r.x+i] = cell
	}
	r.screen[r.y] = line
	r.x += width
	if r.x >= r.cols {
		r.x = r.cols - 1
		r.wrapNext = true
	}
}

func (r *scrollback) newline(firstCol bool) {
	if r.y == r.bottom {
		r.scrollUp(1)
	} else if r.y < r.rows-1 {
		r.y++
	}
	if firstCol {
		r.x = 0
	}
	r.wrapNext = false
}

func (r *scrollback) reverseIndex() {
	if r.y == r.top {
		r.scrollDown(1)
	} else if r.y > 0 {
		r.y--
	}
	r.wrapNext = false
}

func (r *scrollback) scrollUp(n int) {
	if n < 1 {
		return
	}
	if n > r.bottom-r.top+1 {
		n = r.bottom - r.top + 1
	}
	for i := 0; i < n; i++ {
		if r.top == 0 {
			r.pushHistory(r.screen[0])
		}
		for y := r.top; y < r.bottom; y++ {
			r.screen[y] = r.screen[y+1]
		}
		r.screen[r.bottom] = blankCopyLine(r.cols)
	}
}

func (r *scrollback) scrollDown(n int) {
	if n < 1 {
		return
	}
	if n > r.bottom-r.top+1 {
		n = r.bottom - r.top + 1
	}
	for i := 0; i < n; i++ {
		for y := r.bottom; y > r.top; y-- {
			r.screen[y] = r.screen[y-1]
		}
		r.screen[r.top] = blankCopyLine(r.cols)
	}
}

func (r *scrollback) clearScreen(mode int) {
	switch mode {
	case 0:
		r.eraseLine(0)
		for y := r.y + 1; y < r.rows; y++ {
			r.screen[y] = blankCopyLine(r.cols)
		}
	case 1:
		for y := 0; y < r.y; y++ {
			r.screen[y] = blankCopyLine(r.cols)
		}
		r.eraseLine(1)
	case 3:
		r.lines = nil
		fallthrough
	default:
		for y := 0; y < r.rows; y++ {
			r.screen[y] = blankCopyLine(r.cols)
		}
		r.x, r.y = 0, 0
		r.wrapNext = false
	}
}

func (r *scrollback) eraseLine(mode int) {
	line := r.screen[r.y]
	blank := blankCopyCell()
	switch mode {
	case 0:
		for x := r.x; x < len(line.cells); x++ {
			line.cells[x] = blank
		}
	case 1:
		for x := 0; x <= r.x && x < len(line.cells); x++ {
			line.cells[x] = blank
		}
	default:
		for x := range line.cells {
			line.cells[x] = blank
		}
	}
	r.screen[r.y] = line
}

func (r *scrollback) eraseChars(n int) {
	if n < 1 {
		return
	}
	line := r.screen[r.y]
	blank := blankCopyCell()
	for i := 0; i < n && r.x+i < len(line.cells); i++ {
		line.cells[r.x+i] = blank
	}
	r.screen[r.y] = line
}

func (r *scrollback) insertChars(n int) {
	if n < 1 {
		return
	}
	line := r.screen[r.y]
	for i := 0; i < n; i++ {
		if r.x >= len(line.cells) {
			break
		}
		line.cells = append(line.cells, blankCopyCell())
		copy(line.cells[r.x+1:], line.cells[r.x:])
		line.cells[r.x] = blankCopyCell()
		line.cells = line.cells[:len(line.cells)-1]
	}
	r.screen[r.y] = line
}

func (r *scrollback) deleteChars(n int) {
	if n < 1 {
		return
	}
	line := r.screen[r.y]
	for i := 0; i < n; i++ {
		if r.x >= len(line.cells) {
			break
		}
		copy(line.cells[r.x:], line.cells[r.x+1:])
		line.cells[len(line.cells)-1] = blankCopyCell()
	}
	r.screen[r.y] = line
}

func (r *scrollback) insertLines(n int) {
	if n < 1 || r.y < r.top || r.y > r.bottom {
		return
	}
	if n > r.bottom-r.y+1 {
		n = r.bottom - r.y + 1
	}
	for i := 0; i < n; i++ {
		for y := r.bottom; y > r.y; y-- {
			r.screen[y] = r.screen[y-1]
		}
		r.screen[r.y] = blankCopyLine(r.cols)
	}
}

func (r *scrollback) deleteLines(n int) {
	if n < 1 || r.y < r.top || r.y > r.bottom {
		return
	}
	if n > r.bottom-r.y+1 {
		n = r.bottom - r.y + 1
	}
	for i := 0; i < n; i++ {
		for y := r.y; y < r.bottom; y++ {
			r.screen[y] = r.screen[y+1]
		}
		r.screen[r.bottom] = blankCopyLine(r.cols)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// compile-time check: scrollback is an io.Writer for io.TeeReader.
var _ io.Writer = (*scrollback)(nil)
