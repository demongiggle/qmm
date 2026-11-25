package server

import (
	"bytes"
	"strings"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	prefixTimeout = 2 * time.Second
	// repeatTimeout is the tmux-style window in which a repeatable binding
	// can be fired again without pressing the prefix. It is long enough for
	// terminal key auto-repeat, but short enough to return Ctrl-arrow to the
	// pane soon after the user releases the chord.
	repeatTimeout = 500 * time.Millisecond
	prefixByte    = 0x01
)

// handleClientMessage dispatches one client message. It returns true when the
// server should shut down (confirmed prefix+&, qmm kill-server, or a
// session-ending pane exit).
func (s *Server) handleClientMessage(in clientMsg) bool {
	c := in.cli
	// A config recovery warning is modal: only lifecycle and input messages
	// are handled. Everything else is swallowed rather than changing the
	// session underneath the warning.
	if c.configWarningOpen {
		switch in.msg.(type) {
		case protocol.Input, protocol.Resize, protocol.Detach, protocol.Shutdown, forceKillPane:
		default:
			return false
		}
	}
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
	// While the settings page is open, mouse/action messages dismiss the
	// overlay instead of changing panes or windows underneath it.
	if c.settingsOpen {
		switch in.msg.(type) {
		case protocol.Input, protocol.Resize, protocol.Detach, protocol.Shutdown, forceKillPane:
		default:
			s.closeSettings(c)
			return false
		}
	}
	// While pane numbers are shown, mouse/action messages dismiss the
	// transient overlay instead of focusing or resizing panes underneath it.
	if c.displayPanesOpen {
		switch in.msg.(type) {
		case protocol.Input, protocol.Resize, protocol.Detach, protocol.Shutdown, forceKillPane:
		default:
			s.closeDisplayPanes(c)
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
	if s.sess != nil && !s.sess.opts().doubleClickZoom() {
		return
	}
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
		if s.sess != nil {
			delta = s.sess.opts().resizeStepCells()
		}
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
	seq    string
	act    func(s *Server, c *clientConn)
	repeat bool
}

func prefixBind(seq string, act func(s *Server, c *clientConn)) prefixBinding {
	return prefixBinding{seq: seq, act: act}
}

func repeatableBind(seq string, act func(s *Server, c *clientConn)) prefixBinding {
	return prefixBinding{seq: seq, act: act, repeat: true}
}

func repeatableResizeBind(seq string, direction uint8) prefixBinding {
	return repeatableBind(seq, func(s *Server, _ *clientConn) {
		s.resizePane(direction, 0)
	})
}

// Split keys mirror the divider they create: "|" puts a vertical divider
// between left/right panes, "-" a horizontal divider between top/bottom ones.
var prefixBindings = []prefixBinding{
	prefixBind("|", func(s *Server, c *clientConn) { s.splitActive(layout.Vertical) }),
	prefixBind("-", func(s *Server, c *clientConn) { s.splitActive(layout.Horizontal) }),
	prefixBind("c", func(s *Server, c *clientConn) { s.newWindow() }),
	prefixBind("n", func(s *Server, c *clientConn) { s.cycleWindow(1) }),
	prefixBind("p", func(s *Server, c *clientConn) { s.cycleWindow(-1) }),
	prefixBind("x", func(s *Server, c *clientConn) { s.killActivePane() }),
	// tmux uses prefix+& for a confirmed destructive close. qmm has one
	// session, so the chord normally asks before shutting down the whole
	// server; the settings page can turn that confirmation off.
	prefixBind("&", func(s *Server, c *clientConn) {
		if s.sess != nil && !s.sess.opts().confirmShutdown() {
			s.requestShutdown()
			return
		}
		s.openShutdownConfirm(c)
	}),
	prefixBind("o", func(s *Server, c *clientConn) { s.cyclePane() }),
	// tmux-style display-panes: number every pane, then accept the number.
	prefixBind("q", func(s *Server, c *clientConn) { s.openDisplayPanes(c) }),
	// Categorized, scrollable session settings.
	prefixBind("s", func(s *Server, c *clientConn) { s.openSettings(c) }),
	prefixBind("z", func(s *Server, c *clientConn) { s.toggleZoom() }),
	prefixBind("d", func(s *Server, c *clientConn) { s.detachClient(c, "detached") }),
	prefixBind("?", func(s *Server, c *clientConn) { s.toggleHelp(c) }),
	prefixBind("\x01", func(s *Server, c *clientConn) { s.writeRaw(c, []byte{0x01}) }),
	// Ctrl+Arrow (xterm encoding 1;5, rxvt-style 5). These are repeatable so
	// holding Ctrl and pressing an arrow again resizes without another prefix.
	repeatableResizeBind("\x1b[1;5A", protocol.DirUp),
	repeatableResizeBind("\x1b[1;5B", protocol.DirDown),
	repeatableResizeBind("\x1b[1;5C", protocol.DirRight),
	repeatableResizeBind("\x1b[1;5D", protocol.DirLeft),
	repeatableResizeBind("\x1b[5A", protocol.DirUp),
	repeatableResizeBind("\x1b[5B", protocol.DirDown),
	repeatableResizeBind("\x1b[5C", protocol.DirRight),
	repeatableResizeBind("\x1b[5D", protocol.DirLeft),
}

func (s *Server) writeRaw(c *clientConn, data []byte) {
	p := s.sess.activePane()
	if p == nil {
		return
	}
	_, _ = p.ptmx.Write(data)
}

func (s *Server) handleInput(c *clientConn, data []byte) {
	if c.configWarningOpen {
		c.clearRepeat()
		s.handleConfigWarningInput(c, data)
		return
	}
	if c.confirmOpen {
		c.clearRepeat()
		s.handleShutdownConfirmInput(c, data)
		return
	}
	if c.settingsOpen {
		c.clearRepeat()
		s.handleSettingsInput(c, data)
		return
	}
	// A number typed after the overlay's timeout belongs to the pane again.
	if c.displayPanesOpen && s.displayPanesExpired(c) {
		s.expireDisplayPanesFor(c)
	}
	if c.displayPanesOpen {
		c.clearRepeat()
		s.handleDisplayPanesInput(c, data)
		return
	}
	if c.helpOpen {
		c.clearRepeat()
		s.handleHelpInput(c, data)
		return
	}
	if c.repeatExpired() {
		c.clearRepeat()
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
					c.clearRepeat()
					c.enqueue(protocol.Notice{Text: "qmm: unbound prefix key"})
					c.prefixArmed = false
					c.prefixBuf = nil
				}
				continue
			}
		}
		if c.repeatArmed {
			seq := repeatableBindingAt(data[i:])
			switch {
			case c.repeatExpired():
				c.clearRepeat()
			case seq != "":
				flush()
				fireBinding(s, c, seq)
				i += len(seq) - 1
				continue
			case b != prefixByte && isPrefixOfRepeatableBinding(data[i:]):
				// A partial escape sequence is ordinary pane input. Client
				// key events arrive as one complete sequence per message.
				_, _ = pending.Write(data[i:])
				c.clearRepeat()
				i = len(data)
				continue
			default:
				// A new prefix or any other key leaves repeat mode.
				c.clearRepeat()
			}
		}
		if b == prefixByte {
			flush()
			c.clearRepeat()
			c.prefixArmed = true
			c.prefixAt = time.Now()
			c.prefixBuf = c.prefixBuf[:0]
			continue
		}
		_ = pending.WriteByte(b)
	}
	flush()
}

// repeatableBindingAt returns the longest repeatable sequence starting at
// data. An empty string means no repeatable key begins there.
func repeatableBindingAt(data []byte) string {
	longest := ""
	for _, b := range prefixBindings {
		if !b.repeat || len(b.seq) > len(data) || len(b.seq) <= len(longest) {
			continue
		}
		if string(data[:len(b.seq)]) == b.seq {
			longest = b.seq
		}
	}
	return longest
}

func isPrefixOfRepeatableBinding(data []byte) bool {
	seq := string(data)
	for _, b := range prefixBindings {
		if b.repeat && strings.HasPrefix(b.seq, seq) {
			return true
		}
	}
	return false
}

func (c *clientConn) repeatExpired() bool {
	return c.repeatArmed && time.Since(c.repeatAt) > repeatTimeout
}

func (c *clientConn) clearRepeat() {
	c.repeatArmed = false
	c.repeatAt = time.Time{}
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
			if b.repeat {
				c.repeatArmed = true
				c.repeatAt = time.Now()
			} else {
				c.clearRepeat()
			}
			return
		}
	}
}
