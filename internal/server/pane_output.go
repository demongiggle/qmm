package server

import (
	"bufio"
	"io"
)

// readLoop owns the PTY byte stream. vt10x supplies the live screen and
// cursor. The scrollback recorder retains rows evicted from its main screen
// and receives a fresh VT snapshot after each successful Parse.
func (p *pane) readLoop(exitCh chan int, onOutput func()) {
	// Tee the raw PTY bytes into the scrollback recorder while vt10x parses
	// them. The recorder only models line scrolling; the copy-mode buffer gets
	// its visible rows directly from vt10x below.
	br := bufio.NewReaderSize(io.TeeReader(p.ptmx, p.sb), 32*1024)
	for {
		if err := p.vt.Parse(br); err != nil {
			p.notifyExit(exitCh)
			return
		}
		// Resync the recorder with vt10x after every parsed chunk. The
		// recorder only needs to notice lines that scroll off; starting each
		// chunk from the real screen keeps uncommon escape sequences from
		// accumulating drift.
		lines, cx, cy := p.vtSnapshot()
		p.sb.SetScreen(lines, cx, cy)
		// Drain the entire buffered PTY chunk before scheduling a render so
		// applications that hide/redraw/show the cursor in one write (such
		// as watch or ncurses) don't emit intermediate cursor-hidden frames.
		if br.Buffered() == 0 && onOutput != nil {
			onOutput()
		}
	}
}

// copyBuffer returns scrollback plus the live screen, oldest line first.
func (p *pane) copyBuffer() []copyLine {
	lines, _ := p.copyBufferAtCursor()
	return lines
}

// copyBufferAtCursor returns the copy buffer along with the pane's cursor
// mapped into it. Copy mode starts on that point, so entering it lands on the
// line the user was typing at instead of the bottom of the buffer.
func (p *pane) copyBufferAtCursor() ([]copyLine, copyPoint) {
	if p == nil {
		return nil, copyPoint{}
	}
	lines := []copyLine{}
	if p.sb != nil {
		lines = append(lines, p.sb.History()...)
	}
	history := len(lines)
	visible, curX, curY := p.vtSnapshot()
	lines = append(lines, visible...)
	// The live screen sits at the bottom of the buffer, so the cursor's screen
	// row maps onto the same row of the snapshot regardless of scrollback.
	cursor := copyPoint{line: history + curY, col: curX}
	return lines, cursor
}
