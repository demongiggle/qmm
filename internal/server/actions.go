package server

import (
	"bytes"
	"strings"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
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
	// forceKillPane is an internal escalation from a timer, so it arrives
	// without a client. Handle it before any per-client state: dereferencing
	// the nil client used to panic the event loop two seconds after prefix+x
	// and take every pane down with the server.
	if msg, ok := in.msg.(forceKillPane); ok {
		s.killPaneNow(msg.paneID)
		return s.shutdownRequested
	}
	c := in.cli
	s.cancelInterruptedGestures(c, in.msg)
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
			// Snapshotted selections no longer match the layout.
			s.dropSelections()
		}
		s.broadcast()
	case protocol.Detach:
		s.detachClient(c, "detached")
	case protocol.Shutdown:
		s.requestShutdown()
		return true
	default:
		if s.routeOverlay(c, in.msg) {
			return false
		}
		s.handleAction(c, in.msg)
	}
	return s.shutdownRequested
}

// handleAction applies a message that passed the client-local interaction rules.
func (s *Server) handleAction(c *clientConn, action any) {
	switch msg := action.(type) {
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
		s.dragEnd(c, msg.Splitter)
	case protocol.ActionWheel:
		s.handleWheel(c, msg.Delta)
	case protocol.ActionSelectStart:
		s.handleSelectStart(c, msg.X, msg.Y)
	case protocol.ActionSelectUpdate:
		s.handleSelectUpdate(c, msg.X, msg.Y)
	case sessionListResult:
		s.handleSessionListResult(c, msg)
	}
}

// forceKillPane is an internal message used to escalate a kill after a timeout.
type forceKillPane struct{ paneID int }

// killPaneNow escalates a pane that ignored SIGHUP to SIGKILL. It runs in the
// event loop; the pane may already have exited, in which case there is nothing
// left to do.
func (s *Server) killPaneNow(paneID int) {
	p, ok := s.sess.panes[paneID]
	if !ok {
		return
	}
	if p.ptmx != nil {
		_ = p.ptmx.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

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

// switchClientSession instructs c to disconnect and attach to (or create)
// targetName, leaving this session running untouched.
func (s *Server) switchClientSession(c *clientConn, targetName string, create bool) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	c.enqueue(protocol.SwitchSession{Name: targetName, Create: create})
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
		s.sess.setActiveWindow(i)
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
	s.sess.setActiveWindow(idxs[pos])
	s.broadcast()
}

// lastWindow switches back to the window that was active immediately before
// the current one (tmux last-window, prefix+l). Because every switch records
// the previous window, pressing l twice toggles between the two windows. A
// stale pointer — the last window's panes all exited in the meantime — is
// cleared so the chord becomes an accepted no-op.
func (s *Server) lastWindow() {
	if s.sess.last < 0 || s.sess.last == s.sess.active {
		return
	}
	i := s.sess.last
	if _, ok := s.sess.windows[i]; !ok {
		s.sess.last = -1
		return
	}
	s.sess.setActiveWindow(i)
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
	// tmux-style last-window: toggle back to the previously active window.
	prefixBind("l", func(s *Server, c *clientConn) { s.lastWindow() }),
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
	// tmux-style copy mode: prefix+[ enters a vim-keyed scrollback view;
	// prefix+] writes the last copied selection into the active pane.
	prefixBind("[", func(s *Server, c *clientConn) { s.openCopyMode(c, 0) }),
	prefixBind("]", func(s *Server, c *clientConn) { s.pasteClipboard(c) }),
	// Categorized, scrollable session settings.
	prefixBind("s", func(s *Server, c *clientConn) { s.openSettings(c) }),
	// Multi-session switcher and creator (Shift-S pairs with lowercase s).
	prefixBind("S", func(s *Server, c *clientConn) { s.openSessionsMenu(c) }),
	// Saved per-window layouts: restore into a new window, save, or delete.
	prefixBind("L", func(s *Server, c *clientConn) { s.openLayoutsMenu(c) }),
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

// Digits jump to a window by its status-bar number. When the digit could start
// a longer existing index, the jump waits for the next digit or a short timeout
// (see window_goto.go), mirroring prefix+q's pane-number input.
func init() {
	for d := '0'; d <= '9'; d++ {
		digit := int(d - '0')
		prefixBindings = append(prefixBindings, prefixBind(string(d), func(s *Server, c *clientConn) {
			s.openWindowGoto(c, digit)
		}))
	}
}

func (s *Server) writeRaw(c *clientConn, data []byte) {
	p := s.sess.activePane()
	if p == nil {
		return
	}
	_, _ = p.ptmx.Write(data)
}

// paneAppCursor reports whether the active pane's application wants cursor
// keys (arrows, Home, End) in SS3 application form (ESC O X) instead of the
// normal CSI form (ESC [ X). Full-screen programs and line editors like zsh
// enable DECCKM (via smkx, ESC[?1h) and then expect khome/kend/kcuu1 etc.
// from terminfo (ESC O H/F/A...). qmm's client always sends the normal CSI
// form, so translate here based on the pane's tracked VT mode. Modified
// keys (ESC[1;5A etc.) stay in CSI form in both modes, matching terminfo.
func (s *Server) paneAppCursor() bool {
	if s.sess == nil {
		return false
	}
	p := s.sess.activePane()
	if p == nil || p.vt == nil {
		return false
	}
	p.vt.Lock()
	mode := p.vt.Mode()
	p.vt.Unlock()
	return mode&vt10x.ModeAppCursor != 0
}

// translateAppCursor rewrites unmodified CSI cursor-key sequences to their
// SS3 application-mode equivalents. Only the six plain three-byte sequences
// are touched; parameterized (modified) sequences pass through unchanged.
func translateAppCursor(data []byte, app bool) []byte {
	if !app || len(data) == 0 {
		return data
	}
	// Fast path: no ESC, nothing to translate.
	hasEsc := false
	for _, b := range data {
		if b == 0x1b {
			hasEsc = true
			break
		}
	}
	if !hasEsc {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if i+2 < len(data) && data[i] == 0x1b && data[i+1] == '[' {
			switch data[i+2] {
			case 'A', 'B', 'C', 'D', 'H', 'F':
				out = append(out, 0x1b, 'O', data[i+2])
				i += 2
				continue
			}
		}
		out = append(out, data[i])
	}
	return out
}

// handlePaneInput processes the byte stream after client-local overlays.
func (s *Server) handlePaneInput(c *clientConn, data []byte) {
	// A pending numeric window jump owns the next digits; a stale one commits
	// before ordinary input resumes.
	if c.windowGotoOpen && s.windowGotoExpired(c) {
		s.commitWindowGoto(c)
	}
	if c.repeatExpired() {
		c.clearRepeat()
	}
	var pending bytes.Buffer
	flush := func() {
		if pending.Len() == 0 {
			return
		}
		s.writeRaw(c, translateAppCursor(pending.Bytes(), s.paneAppCursor()))
		pending.Reset()
	}
	for i := 0; i < len(data); i++ {
		b := data[i]
		if c.windowGotoOpen {
			if s.windowGotoExpired(c) {
				s.commitWindowGoto(c)
				// fall through: the byte is ordinary input after the commit
			} else if s.consumeWindowGotoInput(c, data[i:]) {
				continue
			}
		}
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
