package server

import (
	"bytes"
	"strings"
	"time"

	"github.com/demongiggle/cogito/internal/layout"
	"github.com/demongiggle/cogito/internal/protocol"
)

const prefixTimeout = 2 * time.Second
const prefixByte = 0x01

// handleClientMessage dispatches one client message. It returns true when the
// server should shut down (confirmed prefix+&, cogito kill-server, or a
// session-ending pane exit).
func (s *Server) handleClientMessage(in clientMsg) bool {
	c := in.cli
	// While the shutdown dialog is open, only lifecycle and input messages
	// are handled. Mouse/action messages are swallowed rather than changing
	// the session underneath the pending decision.
	if c.confirmOpen {
		switch in.msg.(type) {
		case protocol.Input, protocol.Resize, protocol.Detach, protocol.Shutdown, forceKillPane:
		default:
			return false
		}
	}
	// While help is open, mouse/action messages dismiss the overlay instead
	// of reaching the panes underneath. Input and control messages are
	// handled below.
	if c.helpOpen {
		switch in.msg.(type) {
		case protocol.Input, protocol.Resize, protocol.Detach, protocol.Shutdown, forceKillPane:
		default:
			s.closeHelp(c)
			return false
		}
	}
	switch msg := in.msg.(type) {
	case protocol.Input:
		s.handleInput(c, msg.Data)
	case protocol.Resize:
		cols, rows := msg.Cols, msg.Rows
		if cols < 1 {
			cols = 1
		}
		if rows < 1 {
			rows = 1
		}
		c.cols, c.rows = cols, rows
		if s.recomputeSize() {
			s.relayout()
			s.needFull = true
		}
		s.broadcast()
	case protocol.Detach:
		s.detachClient(c, "detached")
	case protocol.Shutdown:
		s.requestShutdown()
		return true
	case protocol.ActionFocus:
		s.focusAt(msg.X, msg.Y)
	case protocol.ActionSelectWindow:
		s.selectWindow(msg.Index)
	case protocol.ActionNewWindow:
		s.newWindow()
	case protocol.ActionSplit:
		if msg.Vertical {
			s.splitActive(layout.Vertical)
		} else {
			s.splitActive(layout.Horizontal)
		}
	case protocol.ActionKillPane:
		s.killActivePane()
	case protocol.ActionNextWindow:
		s.cycleWindow(1)
	case protocol.ActionPrevWindow:
		s.cycleWindow(-1)
	case protocol.ActionCyclePane:
		s.cyclePane()
	case protocol.ActionToggleZoom:
		s.toggleZoom()
	case protocol.ActionTogglePaneZoom:
		s.togglePaneZoom(msg.PaneID)
	case protocol.ActionResizePane:
		s.resizePane(msg.Direction, msg.Delta)
	case protocol.ActionDragStart:
		s.dragStart(c, msg.Splitter)
	case protocol.ActionDragUpdate:
		s.dragUpdate(c, msg.Splitter, msg.Delta)
	case protocol.ActionDragEnd:
		c.dragActive = false
	case forceKillPane:
		if p, ok := s.sess.panes[msg.paneID]; ok {
			_ = p.ptmx.Close()
			if p.cmd != nil && p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}
	return s.shutdownRequested
}

// forceKillPane is an internal message used to escalate a kill after a timeout.
type forceKillPane struct{ paneID int }

func (s *Server) contentRect() layout.Rect {
	return layout.Rect{X: 0, Y: 0, W: s.cols, H: s.rows - statusRows}
}

func (s *Server) detachClient(c *clientConn, reason string) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	// Queue Goodbye behind any already-pending frames, then close shortly
	// after to give the writer a chance to flush it.
	c.enqueue(protocol.Goodbye{Reason: reason})
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = c.conn.Close()
		close(c.done)
	}()
	if s.recomputeSize() {
		s.relayout()
		s.needFull = true
	}
	s.broadcast()
}

func (s *Server) dropClient(c *clientConn) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	_ = c.conn.Close()
	close(c.done)
	if s.recomputeSize() {
		s.relayout()
		s.needFull = true
	}
	s.broadcast()
}

func (s *Server) activeWindow() *window { return s.sess.activeWindow() }

func (s *Server) focusAt(x, y int) {
	w := s.activeWindow()
	if w == nil || w.zoomed {
		return
	}
	bounds := s.contentRect()
	for id, r := range w.tree.Compute(bounds) {
		if r.Contains(x, y) {
			if id != w.activePane {
				w.activePane = id
				s.broadcast()
			}
			return
		}
	}
}

func (s *Server) selectWindow(i int) {
	if _, ok := s.sess.windows[i]; !ok {
		return
	}
	if s.sess.active != i {
		s.sess.active = i
		s.broadcast()
	}
}

func (s *Server) newWindow() {
	cols, rows := s.cols, s.rows-statusRows
	if _, err := s.sess.newWindow(cols, rows); err != nil {
		return
	}
	s.relayout()
	s.broadcast()
}

func (s *Server) splitActive(dir layout.Direction) {
	if _, err := s.sess.splitActivePane(s.cols, s.rows-statusRows, dir); err != nil {
		return
	}
	s.relayout()
	s.needFull = true // borders moved everywhere
	s.broadcast()
}

func (s *Server) killActivePane() {
	w := s.activeWindow()
	if w == nil {
		return
	}
	p := s.sess.panes[w.activePane]
	if p == nil {
		return
	}
	p.kill()
	// If the pane survives a SIGHUP for 2 seconds, SIGKILL the shell itself.
	paneID := p.id
	time.AfterFunc(2*time.Second, func() {
		panicSafe(func() { s.msgs <- clientMsg{msg: forceKillPane{paneID: paneID}} })
	})
}

func panicSafe(f func()) {
	defer func() { _ = recover() }()
	f()
}

func (s *Server) cyclePane() {
	w := s.activeWindow()
	if w == nil {
		return
	}
	next := w.tree.Next(w.activePane)
	if next == w.activePane {
		s.broadcast()
		return
	}
	w.activePane = next
	// The new active pane must own the floating overlay's PTY size.
	if w.zoomed {
		s.relayout()
		s.needFull = true
	}
	s.broadcast()
}

func (s *Server) toggleZoom() {
	w := s.activeWindow()
	if w == nil {
		return
	}
	w.zoomed = !w.zoomed
	s.relayout()
	s.needFull = true
	s.broadcast()
}

// togglePaneZoom handles a mouse double-click. Double-clicking the floating
// pane restores the split; double-clicking an exposed pane zooms that pane.
func (s *Server) togglePaneZoom(paneID int) {
	w := s.activeWindow()
	if w == nil || !w.tree.Has(paneID) || s.sess.panes[paneID] == nil {
		return
	}
	if w.zoomed && w.activePane == paneID {
		w.zoomed = false
	} else {
		w.zoomed = true
		w.activePane = paneID
	}
	s.relayout()
	s.needFull = true
	s.broadcast()
}

func (s *Server) cycleWindow(delta int) {
	idxs := s.sess.sortedWindowIndices()
	if len(idxs) < 2 {
		return
	}
	pos := 0
	for i, i2 := range idxs {
		if i2 == s.sess.active {
			pos = i
			break
		}
	}
	pos = (pos + delta + len(idxs)) % len(idxs)
	s.sess.active = idxs[pos]
	s.broadcast()
}

func (s *Server) resizePane(direction uint8, delta int) {
	w := s.activeWindow()
	if w == nil {
		return
	}
	var dir string
	switch direction {
	case protocol.DirUp:
		dir = layout.Up
	case protocol.DirDown:
		dir = layout.Down
	case protocol.DirLeft:
		dir = layout.Left
	case protocol.DirRight:
		dir = layout.Right
	default:
		return
	}
	if delta == 0 {
		delta = 2
	}
	if w.tree.Resize(w.activePane, dir, delta, s.contentRect()) {
		s.relayout()
		s.needFull = true
		s.broadcast()
	}
}

func (s *Server) dragStart(c *clientConn, splitterID int) {
	w := s.activeWindow()
	if w == nil {
		return
	}
	for _, b := range w.tree.Borders(s.contentRect()) {
		if b.ID == splitterID {
			c.dragID = splitterID
			c.dragActive = true
			return
		}
	}
}

func (s *Server) dragUpdate(c *clientConn, splitterID, delta int) {
	if !c.dragActive || c.dragID != splitterID || delta == 0 {
		return
	}
	w := s.activeWindow()
	if w == nil {
		return
	}
	if w.tree.ResizeSplit(splitterID, delta, s.contentRect()) {
		s.relayout()
		s.needFull = true
		s.broadcast()
	}
}

// --- input byte stream and prefix machine ---------------------------------

type prefixBinding struct {
	seq string
	act func(s *Server, c *clientConn)
}

// Split keys mirror the divider they create: "|" puts a vertical divider
// between left/right panes, "-" a horizontal divider between top/bottom ones.
var prefixBindings = []prefixBinding{
	{"|", func(s *Server, c *clientConn) { s.splitActive(layout.Vertical) }},
	{"-", func(s *Server, c *clientConn) { s.splitActive(layout.Horizontal) }},
	{"c", func(s *Server, c *clientConn) { s.newWindow() }},
	{"n", func(s *Server, c *clientConn) { s.cycleWindow(1) }},
	{"p", func(s *Server, c *clientConn) { s.cycleWindow(-1) }},
	{"x", func(s *Server, c *clientConn) { s.killActivePane() }},
	// tmux uses prefix+& for a confirmed destructive close. cogito has one
	// session, so the chord asks before shutting down the whole server.
	{"&", func(s *Server, c *clientConn) { s.openShutdownConfirm(c) }},
	{"o", func(s *Server, c *clientConn) { s.cyclePane() }},
	{"z", func(s *Server, c *clientConn) { s.toggleZoom() }},
	{"d", func(s *Server, c *clientConn) { s.detachClient(c, "detached") }},
	{"?", func(s *Server, c *clientConn) { s.toggleHelp(c) }},
	{"\x01", func(s *Server, c *clientConn) { s.writeRaw(c, []byte{0x01}) }},
	// Ctrl+Arrow (xterm encoding 1;5, rxvt-style 5).
	{"\x1b[1;5A", func(s *Server, c *clientConn) { s.resizePane(protocol.DirUp, 2) }},
	{"\x1b[1;5B", func(s *Server, c *clientConn) { s.resizePane(protocol.DirDown, 2) }},
	{"\x1b[1;5C", func(s *Server, c *clientConn) { s.resizePane(protocol.DirRight, 2) }},
	{"\x1b[1;5D", func(s *Server, c *clientConn) { s.resizePane(protocol.DirLeft, 2) }},
	{"\x1b[5A", func(s *Server, c *clientConn) { s.resizePane(protocol.DirUp, 2) }},
	{"\x1b[5B", func(s *Server, c *clientConn) { s.resizePane(protocol.DirDown, 2) }},
	{"\x1b[5C", func(s *Server, c *clientConn) { s.resizePane(protocol.DirRight, 2) }},
	{"\x1b[5D", func(s *Server, c *clientConn) { s.resizePane(protocol.DirLeft, 2) }},
}

func (s *Server) writeRaw(c *clientConn, data []byte) {
	p := s.sess.activePane()
	if p == nil {
		return
	}
	_, _ = p.ptmx.Write(data)
}

func (s *Server) handleInput(c *clientConn, data []byte) {
	if c.confirmOpen {
		s.handleShutdownConfirmInput(c, data)
		return
	}
	if c.helpOpen {
		s.handleHelpInput(c, data)
		return
	}
	var pending bytes.Buffer
	flush := func() {
		if pending.Len() == 0 {
			return
		}
		s.writeRaw(c, pending.Bytes())
		pending.Reset()
	}
	for i := 0; i < len(data); i++ {
		b := data[i]
		if c.prefixArmed {
			if time.Since(c.prefixAt) > prefixTimeout {
				c.prefixArmed = false
				c.prefixBuf = nil
				// fall through: handle b as ordinary input
			} else {
				c.prefixBuf = append(c.prefixBuf, b)
				seq := string(c.prefixBuf)
				switch {
				case isExactBinding(seq):
					pending.Reset()
					fireBinding(s, c, seq)
					c.prefixArmed = false
					c.prefixBuf = nil
				case isPrefixOfBinding(seq):
					// wait for more bytes
				default:
					c.enqueue(protocol.Notice{Text: "cogito: unbound prefix key"})
					c.prefixArmed = false
					c.prefixBuf = nil
				}
				continue
			}
		}
		if b == prefixByte {
			flush()
			c.prefixArmed = true
			c.prefixAt = time.Now()
			c.prefixBuf = c.prefixBuf[:0]
			continue
		}
		_ = pending.WriteByte(b)
	}
	flush()
}

func isExactBinding(seq string) bool {
	for _, b := range prefixBindings {
		if b.seq == seq {
			return true
		}
	}
	return false
}

func isPrefixOfBinding(seq string) bool {
	for _, b := range prefixBindings {
		if strings.HasPrefix(b.seq, seq) {
			return true
		}
	}
	return false
}

func fireBinding(s *Server, c *clientConn, seq string) {
	for _, b := range prefixBindings {
		if b.seq == seq {
			b.act(s, c)
			return
		}
	}
}
