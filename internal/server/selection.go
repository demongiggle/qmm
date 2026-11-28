package server

import (
	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

// selectionState is one attached client's mouse text selection: a frozen
// snapshot of a single pane's scrollback plus screen, with a character-wise
// selection on top of it.
//
// A selection is deliberately not copy mode. It never captures the keyboard
// (only the configured copy key is intercepted) and it never swallows a mouse
// action: clicking a pane, a status-bar tab, a splitter, or double-clicking to
// zoom all work as usual and simply cancel the selection first. The pane keeps
// running while the selection is displayed, so the highlighted snapshot cannot
// drift as new output arrives.
type selectionState struct {
	paneID int
	lines  []copyLine
	rect   layout.Rect // pane viewport; fixed for the lifetime of the selection
	top    int         // first buffer line shown in rect
	anchor copyPoint
	cursor copyPoint
}

func (st *selectionState) rows() int {
	return max(1, st.rect.H)
}

// maxTop is the newest viewport position: the one where the snapshot's last
// line sits on the pane's bottom row.
func (st *selectionState) maxTop() int {
	return max(0, len(st.lines)-st.rows())
}

// pointAt maps a screen cell to a buffer position inside the pane viewport.
// Cells outside the pane clamp to its edges, so a drag that leaves the pane
// keeps extending the selection.
func (st *selectionState) pointAt(x, y int) copyPoint {
	if len(st.lines) == 0 {
		return copyPoint{}
	}
	row := clampInt(y-st.rect.Y, 0, st.rows()-1)
	line := clampInt(st.top+row, 0, len(st.lines)-1)
	col := clampInt(x-st.rect.X, 0, lineLenAt(st.lines, line)-1)
	return copyPoint{line: line, col: col}
}

// scrollBy moves the snapshot viewport and the selection endpoint together, so
// the wheel extends the selection into (or back out of) scrollback.
func (st *selectionState) scrollBy(n int) {
	if len(st.lines) == 0 {
		return
	}
	old := st.top
	st.top = clampInt(st.top+n, 0, st.maxTop())
	st.cursor.line = clampInt(st.cursor.line+(st.top-old), 0, len(st.lines)-1)
	st.cursor.col = clampInt(st.cursor.col, 0, lineLenAt(st.lines, st.cursor.line)-1)
}

func (st *selectionState) inSelection(line, col int) bool {
	return pointSelected(selectionChar, st.anchor, st.cursor, line, col)
}

func (st *selectionState) selectedText() string {
	return selectionText(st.lines, selectionChar, st.anchor, st.cursor)
}

// selectionKeepsAlive reports whether msg may leave a pending mouse selection
// in place. Drag updates and wheel steps extend it, and Input is inspected by
// handleInput (the copy key copies, anything else returns to the live pane).
// Every other message cancels the selection before it is applied.
func selectionKeepsAlive(msg interface{}) bool {
	switch msg.(type) {
	case protocol.Input, protocol.ActionWheel, protocol.ActionSelectStart, protocol.ActionSelectUpdate:
		return true
	}
	return false
}

// handleSelectStart anchors a mouse drag selection. A live pane is snapshotted
// into a non-modal selection view; while keyboard copy mode is open the drag
// re-anchors copy mode's own selection instead.
func (s *Server) handleSelectStart(c *clientConn, x, y int) {
	if st := c.copyMode; st != nil && st.open {
		p := st.scrToBuffer(x, y)
		st.selKind = selectionChar
		st.searchActive = false
		st.mouseDrag = true
		st.anchor, st.cursor = p, p
		st.clearCount()
		c.forceFull = true
		s.broadcast()
		return
	}
	s.startSelection(c, x, y)
}

// startSelection snapshots the pressed pane and anchors a selection at (x, y).
// The client focuses the pressed pane before arming a drag, but the pane under
// the pointer wins so a lost or reordered focus message cannot select the
// wrong pane.
func (s *Server) startSelection(c *clientConn, x, y int) {
	if s.sess == nil {
		return
	}
	p, rect, ok := s.paneAtPoint(x, y)
	if !ok || rect.W < 1 || rect.H < 1 {
		return
	}
	lines := p.copyBuffer()
	if len(lines) == 0 {
		return
	}
	st := &selectionState{paneID: p.id, lines: lines, rect: rect}
	// Start at the bottom of the buffer so the visible screen maps 1:1 onto
	// the snapshot the user is looking at.
	st.top = st.maxTop()
	st.anchor = st.pointAt(x, y)
	st.cursor = st.anchor
	c.selection = st
	c.forceFull = true
	s.broadcast()
}

// paneAtPoint returns the pane whose viewport contains (x, y). Points that miss
// every pane (a border, or a stale pointer position) fall back to the active
// pane.
func (s *Server) paneAtPoint(x, y int) (*pane, layout.Rect, bool) {
	rects := s.paneRects()
	for id, r := range rects {
		if !r.Contains(x, y) {
			continue
		}
		if p := s.sess.panes[id]; p != nil {
			return p, r, true
		}
	}
	p := s.sess.activePane()
	if p == nil {
		return nil, layout.Rect{}, false
	}
	r, ok := rects[p.id]
	if !ok {
		return nil, layout.Rect{}, false
	}
	return p, r, true
}

func (s *Server) handleSelectUpdate(c *clientConn, x, y int) {
	if st := c.copyMode; st != nil && st.open {
		if !st.mouseDrag {
			return
		}
		st.cursor = st.scrToBuffer(x, y)
		c.forceFull = true
		s.broadcast()
		return
	}
	st := c.selection
	if st == nil {
		return
	}
	st.cursor = st.pointAt(x, y)
	c.forceFull = true
	s.broadcast()
}

// wheelSelection scrolls the frozen snapshot under a pending mouse selection.
// Wheel-down on the newest line ends the selection, like leaving copy mode.
func (s *Server) wheelSelection(c *clientConn, delta int) {
	st := c.selection
	if st == nil {
		return
	}
	step := clampInt(delta, -20, 20) * wheelStepLines
	if step < 0 {
		st.scrollBy(step)
	} else {
		before := st.top
		st.scrollBy(step)
		if st.top == before {
			s.clearSelection(c)
			return
		}
	}
	c.forceFull = true
	s.broadcast()
}

// copyMouseSelection copies a pending mouse selection and returns to the live
// view. It is the only thing a mouse selection intercepts from the keyboard.
func (s *Server) copyMouseSelection(c *clientConn) {
	st := c.selection
	if st == nil {
		return
	}
	s.deliverClipboard(c, st.selectedText())
	s.clearSelection(c)
}

// clearSelection drops a pending mouse selection and repaints the live view.
func (s *Server) clearSelection(c *clientConn) {
	if c.selection == nil {
		return
	}
	c.selection = nil
	c.forceFull = true
	s.broadcast()
}

// dropSelections clears every client's pending selection. Callers broadcast
// afterwards; it is used when the layout changes under the selections.
func (s *Server) dropSelections() {
	for c := range s.clients {
		if c.selection != nil {
			c.selection = nil
			c.forceFull = true
		}
	}
}

// drawSelection blits a pane's frozen snapshot with the selection highlighted.
// Only the pane's own cells are replaced; the rest of the layout stays live.
func (c *compositor) drawSelection(setCell helpSetCell, st *selectionState, r layout.Rect) {
	for y := 0; y < r.H; y++ {
		idx := st.top + y
		line := copyLine{}
		if idx >= 0 && idx < len(st.lines) {
			line = st.lines[idx]
		}
		blitSnapshotLine(setCell, line, r.X, r.Y+y, r.W, func(col int) (int32, int32, uint8, bool) {
			if st.inSelection(idx, col) {
				return c.chrome.copySelFG, c.chrome.copySelBG, 0, true
			}
			return 0, 0, 0, false
		}, c.chrome.mapColor)
	}
}
