package server

import (
	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

// A splitter drag belongs to the initiating client until dragEnd installs
// its preview in the shared window. relayout then invalidates other previews.
type splitterDrag struct {
	window   *window
	splitter int
	delta    int
	preview  *layout.Tree
}

func (s *Server) dragStart(c *clientConn, splitterID int) {
	w := s.activeWindow()
	if w == nil || w.zoomed || c.drag != nil {
		return
	}
	for _, b := range w.tree.Borders(s.contentRect()) {
		if b.ID == splitterID {
			c.drag = &splitterDrag{window: w, splitter: splitterID}
			return
		}
	}
}

func (s *Server) dragUpdate(c *clientConn, splitterID, delta int) {
	drag := c.drag
	if drag == nil || drag.splitter != splitterID || delta == 0 {
		return
	}
	// Measure from the original layout so returning the pointer also restores
	// the divider after hitting a pane's minimum size.
	drag.delta += delta
	preview := drag.window.tree.Clone()
	if !preview.ResizeSplit(splitterID, drag.delta, s.contentRect()) {
		preview = nil
	}
	if preview == nil && drag.preview == nil {
		return
	}
	drag.preview = preview
	s.broadcast()
}

func (s *Server) dragEnd(c *clientConn, splitterID int) {
	drag := c.drag
	if drag == nil || drag.splitter != splitterID {
		return
	}
	c.drag = nil
	if drag.preview == nil {
		return
	}
	if _, attached := s.clients[c]; !attached {
		return
	}
	drag.window.tree = drag.preview
	s.relayout()
	s.broadcast()
}

func (s *Server) cancelInterruptedDrag(c *clientConn, msg any) {
	drag := c.drag
	if drag == nil {
		return
	}
	switch msg.(type) {
	case protocol.ActionDragStart, protocol.ActionDragUpdate, protocol.ActionDragEnd:
		return
	}
	c.drag = nil
	if drag.preview != nil {
		s.broadcast()
	}
}
