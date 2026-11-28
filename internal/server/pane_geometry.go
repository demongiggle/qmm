package server

import (
	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
)

// paneArea excludes the status bar. A PTY still needs at least one row
// when the attached terminal has only enough space for the bar itself.
func paneArea(cols, rows int) layout.Rect {
	return layout.Rect{W: cols, H: rows - statusRows}
}

func (s *Server) contentRect() layout.Rect { return paneArea(s.cols, s.rows) }

type zoomViewport struct {
	paneID       int
	outer, inner layout.Rect
}

// paneGeometry describes one layout in screen coordinates. Split panes
// remain visible around a floating zoom, but only its inner rect accepts
// selection and sets the active terminal's size.
type paneGeometry struct {
	tree *layout.Tree
	area layout.Rect
	zoom *zoomViewport
}

func (w *window) committedGeometry(area layout.Rect) paneGeometry {
	g := paneGeometry{tree: w.tree, area: area}
	if w.zoomed {
		outer, inner := zoomRects(area.W, area.H)
		g.zoom = &zoomViewport{paneID: w.activePane, outer: outer, inner: inner}
	}
	return g
}

// frameGeometry is the sole reader of a client's proposed tree for rendering.
func (v *clientView) frameGeometry(w *window, area layout.Rect) paneGeometry {
	g := w.committedGeometry(area)
	if drag := v.drag; drag != nil && drag.window == w && drag.preview != nil {
		g.tree = drag.preview
	}
	return g
}

func (g paneGeometry) splitRects() map[int]layout.Rect {
	return g.tree.Compute(g.area)
}

// paneRects are the committed viewports for hit testing, copy and selection.
func (s *Server) paneRects() map[int]layout.Rect {
	w := s.activeWindow()
	if w == nil {
		return nil
	}
	g := w.committedGeometry(s.contentRect())
	if z := g.zoom; z != nil {
		if s.sess.panes[z.paneID] == nil {
			return nil
		}
		return map[int]layout.Rect{z.paneID: z.inner}
	}
	return g.splitRects()
}

// zoomMarginPercent is the margin left on each side of a floating zoomed
// pane, measured against the content area (the status bar stays visible).
const zoomMarginPercent = 2

// zoomRects returns the centered overlay's outer border rectangle and its
// inner content rectangle. Margins are rounded up to whole cells so every
// side keeps at least zoomMarginPercent of the content area.
func zoomRects(cols, rows int) (outer, inner layout.Rect) {
	margin := func(size int) int {
		if size < 1 {
			return 0
		}
		// Integer ceiling of size*zoomMarginPercent/100, with a one-cell
		// minimum. Clamp it so a tiny area still has a border and content.
		m := max(1, (size*zoomMarginPercent+99)/100)
		if maxMargin := (size - 3) / 2; maxMargin >= 0 && m > maxMargin {
			m = maxMargin
		}
		return max(0, m)
	}
	mx, my := margin(cols), margin(rows)
	outer = layout.Rect{X: mx, Y: my, W: max(0, cols-2*mx), H: max(0, rows-2*my)}
	inner = outer
	if outer.W > 1 {
		inner.X++
	}
	if outer.H > 1 {
		inner.Y++
	}
	inner.W, inner.H = max(0, outer.W-2), max(0, outer.H-2)
	return outer, inner
}

func (s *Server) relayout() {
	// A real layout change invalidates any previews based on the old geometry.
	for c := range s.clients {
		c.drag = nil
	}
	area := s.contentRect()
	s.sess.relayout(area.W, max(1, area.H))
}

// relayout applies committed geometry to every terminal, including panes in
// the background of a zoom. Its dimensions already exclude the status bar.
func (s *session) relayout(cols, rows int) {
	area := layout.Rect{W: cols, H: rows}
	for _, w := range s.windows {
		g := w.committedGeometry(area)
		for id, r := range g.splitRects() {
			if p := s.panes[id]; p != nil {
				if z := g.zoom; z != nil && id == z.paneID {
					r = z.inner
				}
				resizePane(p, max(1, r.W), max(1, r.H))
			}
		}
	}
}

func resizePane(p *pane, cols, rows int) {
	if p.ptmx != nil {
		_ = pty.Setsize(p.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
	p.vt.Resize(cols, rows)
	if p.sb != nil {
		p.sb.Resize(cols, rows)
	}
}
