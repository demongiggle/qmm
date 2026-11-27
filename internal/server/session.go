package server

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
)

const (
	minCols    = 40
	minRows    = 10
	statusRows = 1
)

// pane is one shell running inside a PTY, rendered through a virtual terminal.
type pane struct {
	id        int
	cmd       *exec.Cmd
	ptmx      *os.File
	vt        vt10x.Terminal
	sb        *scrollback
	closeOnce sync.Once
}

// window is a tab inside the session, holding a split tree of panes.
type window struct {
	index      int
	tree       *layout.Tree
	activePane int
	zoomed     bool
	name       string
}

// session is one qmm session: a named set of windows.
type session struct {
	name       string
	windows    map[int]*window
	active     int
	nextPaneID int
	panes      map[int]*pane
	settings   settingsState

	// exitCh receives pane IDs whose child process has exited.
	exitCh chan int
	// onOutput is called after a pane produces output; used to schedule a
	// re-render. May be nil in tests.
	onOutput func()
}

func (s *session) sessionName() string {
	if s != nil && s.name != "" {
		return s.name
	}
	return "default"
}

func newSession() *session {
	s := &session{
		windows:  map[int]*window{},
		panes:    map[int]*pane{},
		settings: defaultSettings(),
	}
	return s
}

func (s *session) sortedWindowIndices() []int {
	idx := make([]int, 0, len(s.windows))
	for i := range s.windows {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	return idx
}

// windowPaneIDs returns a window's live pane IDs in creation order. Pane IDs
// are never reused, so sorting gives stable numbers for prefix+q within a
// window even after nested splits.
func (s *session) windowPaneIDs(w *window) []int {
	if w == nil || w.tree == nil {
		return nil
	}
	ids := w.tree.Panes()
	sort.Ints(ids)
	return ids
}

func (s *session) activeWindow() *window { return s.windows[s.active] }

// lowestFreeIndex returns the smallest non-negative unused window index.
func (s *session) lowestFreeIndex() int {
	i := 0
	for {
		if _, ok := s.windows[i]; !ok {
			return i
		}
		i++
	}
}

// fgInfo inspects the foreground process group of the pane's PTY and returns
// its command name and working directory (best effort; both may be empty).
// Note: pgid from TCGETPGRP equals the process group leader's pid; if that
// leader has exited but the group lives on, /proc/<pgid> lookups fail and we
// fall back to "" (the window keeps its previous name).
func fgInfo(p *pane) (name, cwd string) {
	fd := p.ptmx.Fd()
	pgid, err := tcgetpgrp(fd)
	if err != nil || pgid <= 0 {
		return "", ""
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pgid)); err == nil {
		name = strings.TrimSpace(string(b))
	}
	if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid)); err == nil {
		cwd = d
	}
	return name, cwd
}

func tcgetpgrp(fd uintptr) (int, error) {
	pgid := 0
	const tcgetpgrp = 0x540f // Linux ioctl TCGETPGRP
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tcgetpgrp, uintptr(unsafe.Pointer(&pgid)))
	if errno != 0 {
		return 0, errno
	}
	return pgid, nil
}

// spawnPane starts a shell for a new pane with the given working directory.
func (s *session) spawnPane(dir string, cols, rows int) (*pane, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-l")
	// Be explicit: the child gets its own session and controlling tty so a
	// process-group kill aimed at a pane can never hit our own group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "QMM_SESSION="+s.sessionName())
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			cmd.Dir = dir
		}
	}
	if cmd.Dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
	}

	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("spawn shell: %w", err)
	}

	id := s.nextPaneID
	s.nextPaneID++

	p := &pane{
		id:   id,
		cmd:  cmd,
		ptmx: ptmx,
		vt:   vt10x.New(vt10x.WithSize(cols, rows)),
		sb:   newScrollback(cols, rows),
	}
	s.panes[id] = p
	go p.readLoop(s.exitCh, s.onOutput)
	// Reap the child when it exits; the PTY read loop usually notices first
	// (EIO after the last output), so both paths go through notifyExit.
	go func() {
		_ = p.cmd.Wait()
		p.notifyExit(s.exitCh)
	}()
	return p, nil
}

// notifyExit reports the pane's death exactly once.
func (p *pane) notifyExit(ch chan int) {
	p.closeOnce.Do(func() {
		if ch != nil {
			ch <- p.id
		}
	})
}

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

// kill sends SIGHUP to the pane's foreground process group (like a terminal
// hangup) and closes the master side of the PTY. Falls back to signalling the
// direct child if the group signal fails.
func (p *pane) kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		// The child is a session leader (Setsid), so pgid == pid.
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP); err != nil {
			_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGHUP)
		}
	}
	_ = p.ptmx.Close()
}

// newWindow creates and activates a fresh window with one pane.
func (s *session) newWindow(cols, rows int) (*window, error) {
	idx := s.lowestFreeIndex()
	w := &window{index: idx, name: "qmm"}
	p, err := s.spawnPane("", cols, rows)
	if err != nil {
		return nil, err
	}
	w.tree = layout.New(p.id)
	w.activePane = p.id
	s.windows[idx] = w
	s.active = idx
	w.name = fmt.Sprintf("window %d", idx)
	if s.opts().autoNameWindows() {
		if n, _ := fgInfo(p); n != "" {
			w.name = n
		}
	}
	return w, nil
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

// relayout recomputes PTY/vt sizes for every pane of every window.
// cols/rows is the *content* size (excluding the status bar row). Panes keep
// their normal split sizes; in a zoomed window the active pane is resized to
// the floating overlay instead.
func (s *session) relayout(cols, rows int) {
	for _, w := range s.windows {
		bounds := layout.Rect{X: 0, Y: 0, W: cols, H: rows}
		_, zoomInner := zoomRects(cols, rows)
		for id, r := range w.tree.Compute(bounds) {
			p := s.panes[id]
			if p == nil {
				continue
			}
			pw, ph := r.W, r.H
			if w.zoomed && id == w.activePane {
				pw, ph = zoomInner.W, zoomInner.H
			}
			resizePane(p, max(pw, 1), max(ph, 1))
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

// vtSnapshot copies the pane's current vt10x screen and cursor position.
func (p *pane) vtSnapshot() ([]copyLine, int, int) {
	p.vt.Lock()
	defer p.vt.Unlock()
	cols, rows := p.vt.Size()
	cur := p.vt.Cursor()
	out := make([]copyLine, rows)
	for y := 0; y < rows; y++ {
		line := make([]copyCell, cols)
		for x := 0; x < cols; x++ {
			g := p.vt.Cell(x, y)
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			var st uint8
			if g.Mode&gttrBold != 0 {
				st |= protocol.StyleBold
			}
			if g.Mode&gttrItalic != 0 {
				st |= protocol.StyleItalic
			}
			if g.Mode&gttrUnderline != 0 {
				st |= protocol.StyleUnderline
			}
			if g.Mode&gttrBlink != 0 {
				st |= protocol.StyleBlink
			}
			if g.Mode&gttrReverse != 0 {
				st |= protocol.StyleReverse
			}
			line[x] = copyCell{Ch: ch, FG: vtColor(g.FG), BG: vtColor(g.BG), Style: st}
		}
		out[y] = copyLine{cells: line}
	}
	return out, cur.X, cur.Y
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

// activePane returns the active pane of the active window, or nil.
func (s *session) activePane() *pane {
	w := s.activeWindow()
	if w == nil {
		return nil
	}
	return s.panes[w.activePane]
}

// splitActivePane splits the active pane and focuses the new pane. dir is
// layout.Vertical (divider vertical) or layout.Horizontal.
func (s *session) splitActivePane(cols, rows int, dir layout.Direction) (*window, error) {
	w := s.activeWindow()
	if w == nil {
		return nil, fmt.Errorf("no active window")
	}
	// New pane starts in the same working directory as the current one.
	src := s.panes[w.activePane]
	dirPath := ""
	if src != nil {
		_, dirPath = fgInfo(src)
	}
	np, err := s.spawnPane(dirPath, cols, rows)
	if err != nil {
		return nil, err
	}
	if !w.tree.Split(w.activePane, np.id, dir) {
		np.kill()
		delete(s.panes, np.id)
		return nil, fmt.Errorf("split failed")
	}
	w.activePane = np.id
	return w, nil
}
