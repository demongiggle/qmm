package server

import (
	"strings"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
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

// colForRune maps a rune index in text() back to the buffer column that rune
// occupies. Placeholder cells widen lines past their copyable runes, so a
// rune offset (as returned by a search in the copy text) must be converted
// into a terminal column to position the cursor. Values past the last rune
// map to len(cells); callers clamp.
func (l copyLine) colForRune(n int) int {
	if n < 0 {
		return 0
	}
	count := 0
	for i, c := range l.cells {
		if c.Ch == 0 {
			continue
		}
		if count == n {
			return i
		}
		count++
	}
	return len(l.cells)
}

// trimmedCopyLine removes trailing blank cells but keeps at least one cell so
// the line still has a defined width.
func trimmedCopyLine(l copyLine) copyLine {
	width := len(l.cells)
	for width > 1 {
		c := l.cells[width-1]
		if c != blankCopyCell() {
			break
		}
		width--
	}
	if width == len(l.cells) {
		return l
	}
	return copyLine{cells: append([]copyCell(nil), l.cells[:width]...)}
}

// scrollback is a bounded ring of immutable rows. The pane VT lock protects
// appends, clears and snapshots together with the live screen.
type scrollback struct {
	lines  []copyLine
	oldest int
}

func (r *scrollback) AppendRow(grid vt10x.CellGrid, y int) {
	cols, _ := grid.Size()
	line := trimmedCopyLine(snapshotRow(grid, y, cols))
	if len(r.lines) < scrollbackLimit {
		r.lines = append(r.lines, line)
		return
	}
	r.lines[r.oldest] = line
	r.oldest = (r.oldest + 1) % scrollbackLimit
}

func (r *scrollback) Clear() {
	r.lines = nil
	r.oldest = 0
}

// History copies the row headers in chronological order. Their cells remain
// valid after the VT evicts rows or the application clears history.
func (r *scrollback) History() []copyLine {
	lines := make([]copyLine, 0, len(r.lines))
	lines = append(lines, r.lines[r.oldest:]...)
	return append(lines, r.lines[:r.oldest]...)
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
