// Package server implements the qmm session daemon: it owns the PTYs,
// the session state, and the composition of the screen; clients are thin.
package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

// Config controls socket placement.
type Config struct {
	// SocketDir overrides the socket directory; empty uses the default
	// resolution ($QMM_SOCKET_DIR, then $XDG_RUNTIME_DIR/qmm-<uid>,
	// then /tmp/qmm-<uid>).
	SocketDir string
}

// SocketPath resolves the socket path and ensures the directory exists.
func (c Config) SocketPath() (string, error) {
	dir := c.socketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "default"), nil
}

func (c Config) socketDir() string {
	if c.SocketDir != "" {
		return c.SocketDir
	}
	if d := os.Getenv("QMM_SOCKET_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, fmt.Sprintf("qmm-%d", os.Getuid()))
	}
	base := os.TempDir()
	return filepath.Join(base, fmt.Sprintf("qmm-%d", os.Getuid()))
}

// Server is the session daemon. All session state is confined to the
// event loop goroutine; pane goroutines only notify it via channels.
type Server struct {
	cfg  Config
	ln   net.Listener
	sock string
	pid  string

	configPath           string
	configLogPath        string
	pendingConfigWarning *configWarning

	sess *session
	cols int
	rows int

	clients   map[*clientConn]struct{}
	paneExit  chan int
	msgs      chan clientMsg
	newConns  chan *clientConn
	deadConns chan *clientConn
	renderCh  chan struct{}
	shutdown  chan struct{}

	needFull bool
	comp     compositor

	// pasteBuffer is the session's most recent copy. prefix+] writes it into
	// the active pane, like tmux's default paste buffer.
	pasteBuffer string

	// shutdownRequested is set by the confirmed prefix+& chord; Run performs
	// the actual teardown after the current message is handled.
	shutdownRequested bool
}

type clientMsg struct {
	cli *clientConn
	msg interface{}
}

type clientConn struct {
	conn  net.Conn
	codec *protocol.Codec
	cols  int
	rows  int

	done chan struct{}

	sendMu sync.Mutex
	queue  []interface{}
	frame  *protocol.Frame
	notify chan struct{}

	// Border-drag state (client-initiated).
	dragID     int
	dragActive bool

	// Per-client compositor and help-menu state. Help is local to one client.
	comp      compositor
	helpOpen  bool
	helpTop   int
	forceFull bool

	// Shutdown confirmation is local to the client that pressed prefix+&.
	confirmOpen bool

	// prefix+[ opens copy mode: a per-client, vim-keyed view of the active
	// pane's scrollback with an optional selection.
	copyMode *copyModeState

	// selection is a mouse drag selection. It is not copy mode: it never
	// captures the keyboard and never blocks other mouse actions.
	selection *selectionState

	// Config recovery warnings are local. A startup warning is shown once to
	// the first attached client; runtime failures target their triggering
	// client.
	configWarning     configWarning
	configWarningOpen bool

	// prefix+q shows transient pane numbers on one client. The input buffer
	// permits two-digit pane numbers when a window has more than ten panes.
	displayPanesOpen     bool
	displayPanesInput    string
	displayPanesDeadline time.Time

	// prefix+s opens a categorized settings page. Option values are shared,
	// but the cursor and scroll position are local to this client.
	settingsOpen   bool
	settingsCursor int
	settingsTop    int

	// Prefix-key state machine.
	prefixArmed bool
	prefixAt    time.Time
	prefixBuf   []byte

	// Repeatable bindings stay active for a short window, allowing held
	// Ctrl-arrow keys to resize without pressing the prefix again.
	repeatArmed bool
	repeatAt    time.Time
}

// Run starts the server and blocks until shutdown (prefix+&, qmm
// kill-server, or the last pane exiting).
func Run(cfg Config) error {
	s := &Server{
		cfg:       cfg,
		sess:      newSession(),
		cols:      80,
		rows:      24,
		clients:   map[*clientConn]struct{}{},
		paneExit:  make(chan int, 64),
		msgs:      make(chan clientMsg, 128),
		newConns:  make(chan *clientConn, 16),
		deadConns: make(chan *clientConn, 16),
		renderCh:  make(chan struct{}, 1),
		shutdown:  make(chan struct{}, 1),
	}
	s.sess.exitCh = s.paneExit
	s.sess.onOutput = s.scheduleRender
	sock, err := cfg.SocketPath()
	if err != nil {
		return err
	}
	s.sock = sock
	s.initConfig(filepath.Join(filepath.Dir(sock), "server.log"))

	// Serialize concurrent server starts with a lock file held for the
	// server's lifetime. This prevents two daemons from racing to bind
	// (and one unlinking the other's live socket).
	lock, err := os.OpenFile(s.sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open socket lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return fmt.Errorf("another server is already running (lock %s)", s.sock+".lock")
	}
	defer func() { _ = lock.Close() }()

	// The lock is held, so a leftover socket file is stale: only remove it
	// when nothing answers a dial.
	if conn, err := net.DialTimeout("unix", s.sock, 300*time.Millisecond); err == nil {
		_ = conn.Close()
		return fmt.Errorf("server already running at %s", s.sock)
	}
	if err := os.Remove(s.sock); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.sock, err)
	}
	s.ln = ln
	_ = os.Chmod(s.sock, 0o600)
	s.pid = s.sock + ".pid"
	_ = os.WriteFile(s.pid, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	defer func() {
		_ = os.Remove(s.sock)
		_ = os.Remove(s.pid)
	}()

	if _, err := s.sess.newWindow(s.cols, s.rows); err != nil {
		return fmt.Errorf("create initial window: %w", err)
	}

	go s.acceptLoop(s.newConns)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	displayTicker := time.NewTicker(displayPanesExpireInterval)
	defer displayTicker.Stop()

	for {
		var shutdown bool
		select {
		case c := <-s.newConns:
			s.registerClient(c)
		case c := <-s.deadConns:
			s.removeClient(c)
		case in := <-s.msgs:
			shutdown = s.handleClientMessage(in)
		case paneID := <-s.paneExit:
			shutdown = s.handlePaneExit(paneID)
		case <-s.renderCh:
			s.broadcast()
		case <-s.shutdown:
			shutdown = true
		case <-ticker.C:
			s.refreshWindowNames()
			s.broadcast()
		case <-displayTicker.C:
			s.expireDisplayPanes()
		}
		if shutdown {
			s.teardown("session ended")
			return nil
		}
	}
}

func (s *Server) acceptLoop(out chan<- *clientConn) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			c := &clientConn{conn: conn, codec: protocol.NewCodec(conn), notify: make(chan struct{}, 1), done: make(chan struct{}), dragID: -1}
			_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			msg, err := c.codec.Recv()
			if err != nil {
				_ = c.conn.Close()
				return
			}
			_ = c.conn.SetReadDeadline(time.Time{})
			if _, ok := msg.(protocol.Shutdown); ok {
				// Shutdown requested before the handshake completed:
				// take it as a kill-server request.
				select {
				case s.shutdown <- struct{}{}:
				default:
				}
				_ = c.conn.Close()
				return
			}
			hello, ok := msg.(protocol.Hello)
			if !ok {
				_ = c.conn.Close()
				return
			}
			if hello.Cols < 1 || hello.Rows < 1 || hello.Cols > 1000 || hello.Rows > 1000 {
				_ = c.conn.Close()
				return
			}
			c.cols, c.rows = hello.Cols, hello.Rows
			select {
			case out <- c:
			default:
				_ = c.conn.Close()
			}
		}()
	}
}

func (s *Server) registerClient(c *clientConn) {
	s.clients[c] = struct{}{}
	if s.recomputeSize() {
		s.relayout()
	}
	if s.pendingConfigWarning != nil {
		s.showConfigWarning(c, *s.pendingConfigWarning)
		s.pendingConfigWarning = nil
	}
	// Welcome carries the effective size; the following frames tell the rest.
	_ = c.codec.Send(protocol.Welcome{Cols: s.cols, Rows: s.rows})
	s.needFull = true
	go c.sendLoop(s)
	go s.readLoop(c)
	s.broadcast()
}

func (s *Server) readLoop(c *clientConn) {
	defer func() { s.deadConns <- c }()
	for {
		msg, err := c.codec.Recv()
		if err != nil {
			return
		}
		select {
		case s.msgs <- clientMsg{c, msg}:
		default:
			// The event loop is backed up; drop the client rather than block PTY flow.
			return
		}
	}
}

// sendLoop sends queued control messages in order and the pending frame,
// which enqueueFrame keeps up to date by merging incremental updates. It
// exits when the connection breaks and reports to deadConns.
func (c *clientConn) sendLoop(s *Server) {
	for {
		c.sendMu.Lock()
		var msg interface{}
		if len(c.queue) > 0 {
			msg = c.queue[0]
			c.queue = c.queue[1:]
		} else if c.frame != nil {
			msg = c.frame
			c.frame = nil
		}
		c.sendMu.Unlock()

		if msg == nil {
			select {
			case <-c.done:
				return
			case <-c.notify:
			}
			continue
		}
		if err := c.codec.Send(msg); err != nil {
			s.deadConns <- c
			return
		}
	}
}

func (c *clientConn) enqueue(msg interface{}) {
	c.sendMu.Lock()
	c.queue = append(c.queue, msg)
	c.sendMu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// enqueueFrame stores the newest frame for sendLoop. Incremental frames are
// merged with any still-pending frame: the compositor has already advanced
// its diff baseline, so dropping an older frame would lose cells that later
// diffs do not repeat.
func (c *clientConn) enqueueFrame(f protocol.Frame) {
	c.sendMu.Lock()
	if pending := c.frame; pending != nil {
		f = mergeFrames(*pending, f)
	}
	fc := f // copy header; Cells slice is not mutated after handoff
	c.frame = &fc
	c.sendMu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// mergeFrames overlays the newer frame's cells and metadata on an older,
// not-yet-sent frame. A full newer frame supersedes everything before it.
func mergeFrames(old, next protocol.Frame) protocol.Frame {
	if next.Full || old.Cols != next.Cols || old.Rows != next.Rows {
		return next
	}
	merged := next
	merged.Full = old.Full || next.Full
	merged.Cells = make([]protocol.Cell, len(old.Cells), len(old.Cells)+len(next.Cells))
	index := make(map[[2]int]int, len(old.Cells))
	for i, cell := range old.Cells {
		merged.Cells[i] = cell
		index[[2]int{cell.Row, cell.Col}] = i
	}
	for _, cell := range next.Cells {
		key := [2]int{cell.Row, cell.Col}
		if i, ok := index[key]; ok {
			merged.Cells[i] = cell
			continue
		}
		index[key] = len(merged.Cells)
		merged.Cells = append(merged.Cells, cell)
	}
	return merged
}

func (s *Server) removeClient(c *clientConn) { s.dropClient(c) }

// recomputeSize applies the min-of-clients size policy. Reports whether the
// size changed.
func (s *Server) recomputeSize() bool {
	if len(s.clients) == 0 {
		return false
	}
	cols, rows := -1, -1
	for c := range s.clients {
		if cols < 0 || c.cols < cols {
			cols = c.cols
		}
		if rows < 0 || c.rows < rows {
			rows = c.rows
		}
	}
	if cols == s.cols && rows == s.rows {
		return false
	}
	s.cols, s.rows = cols, rows
	return true
}

func (s *Server) relayout() {
	contentRows := s.rows - statusRows
	if contentRows < 1 {
		contentRows = 1
	}
	s.sess.relayout(s.cols, contentRows)
}

// scheduleRender asks the event loop to recompose+send. Non-blocking.
func (s *Server) scheduleRender() {
	select {
	case s.renderCh <- struct{}{}:
	default:
	}
}

func (s *Server) broadcast() {
	forceFull := s.needFull
	s.needFull = false
	// A selection pins the pane viewport it was started on. If the layout
	// moved the pane (split, zoom, window switch, pane exit), the snapshot is
	// stale: drop it rather than draw it over the wrong rectangle.
	var rects map[int]layout.Rect
	for c := range s.clients {
		if sel := c.selection; sel != nil {
			if rects == nil {
				rects = s.paneRects()
			}
			if r, ok := rects[sel.paneID]; !ok || r != sel.rect || s.sess.panes[sel.paneID] == nil {
				c.selection = nil
				c.forceFull = true
			}
		}
		c.comp.helpOpen = c.helpOpen
		c.comp.helpTop = c.helpTop
		c.comp.confirmOpen = c.confirmOpen
		c.comp.configWarning = c.configWarning
		c.comp.configWarningOpen = c.configWarningOpen
		c.comp.displayPanes = c.displayPanesOpen
		c.comp.displayPanesInput = c.displayPanesInput
		c.comp.settingsOpen = c.settingsOpen
		c.comp.settingsCursor = c.settingsCursor
		c.comp.settingsTop = c.settingsTop
		c.comp.copyMode = c.copyMode
		c.comp.selection = c.selection
		frame := c.comp.compose(s.sess, s.cols, s.rows, forceFull || c.forceFull)
		c.forceFull = false
		c.helpTop = c.comp.helpTop
		c.settingsCursor = c.comp.settingsCursor
		c.settingsTop = c.comp.settingsTop
		c.enqueueFrame(frame)
	}
}

func (s *Server) refreshWindowNames() {
	if !s.sess.opts().autoNameWindows() {
		return
	}
	for _, w := range s.sess.windows {
		p := s.sess.panes[w.activePane]
		if p == nil {
			continue
		}
		if n, _ := fgInfo(p); n != "" && n != w.name {
			w.name = n
		}
	}
}

// teardown notifies clients, kills panes and closes the listener. The short
// sleep gives client event loops a chance to flush the Goodbye message
// before their connections are closed.
func (s *Server) teardown(reason string) {
	for c := range s.clients {
		c.enqueue(protocol.Goodbye{Reason: reason})
	}
	time.Sleep(150 * time.Millisecond)
	for c := range s.clients {
		_ = c.conn.Close()
		close(c.done)
	}
	for _, p := range s.sess.panes {
		p.kill()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

// handlePaneExit removes a dead pane; it reports whether the server should
// shut down (no windows remain).
func (s *Server) handlePaneExit(paneID int) bool {
	var target *window
	var targetIdx int
	for idx, w := range s.sess.windows {
		if w.tree.Has(paneID) {
			target, targetIdx = w, idx
			break
		}
	}
	p := s.sess.panes[paneID]
	if p != nil {
		_ = p.ptmx.Close()
		delete(s.sess.panes, paneID)
	}
	if target == nil {
		return false
	}
	if target.zoomed && target.activePane == paneID {
		target.zoomed = false
	}
	target.tree.Remove(paneID)

	if target.tree.Len() == 0 {
		delete(s.sess.windows, targetIdx)
		if len(s.sess.windows) == 0 {
			s.requestShutdown()
			return true
		}
		// Activate the lowest-indexed remaining window.
		idxs := s.sess.sortedWindowIndices()
		s.sess.active = idxs[0]
	} else if target.activePane == paneID {
		if ids := target.tree.Panes(); len(ids) > 0 {
			target.activePane = ids[0]
		}
	}
	s.relayout()
	s.broadcast()
	return false
}
