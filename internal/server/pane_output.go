package server

import (
	"bufio"

	"github.com/demongiggle/qmm/internal/vt10x"
)

func (p *pane) initTerminal(cols, rows int) {
	p.sb.Clear()
	p.vt = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollback(&p.sb))
}

// readLoop feeds the PTY stream to the VT, which records rows as they scroll.
func (p *pane) readLoop(exitCh chan int, onOutput func()) {
	br := bufio.NewReaderSize(p.ptmx, 32*1024)
	for {
		if err := p.vt.Parse(br); err != nil {
			p.notifyExit(exitCh)
			return
		}
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
	p.vt.Lock()
	defer p.vt.Unlock()
	lines := p.sb.History()
	history := len(lines)
	visible, curX, curY := p.snapshotLocked()
	lines = append(lines, visible...)
	// The live screen sits at the bottom of the buffer, so the cursor's screen
	// row maps onto the same row of the snapshot regardless of scrollback.
	cursor := copyPoint{line: history + curY, col: curX}
	return lines, cursor
}
