package server

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/hinshun/vt10x"

	"github.com/creack/pty"

	"github.com/demongiggle/cogito/internal/layout"
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

// session is the single cogito session: a set of windows.
type session struct {
	windows    map[int]*window
	active     int
	nextPaneID int
	panes      map[int]*pane

	// exitCh receives pane IDs whose child process has exited.
	exitCh chan int
	// onOutput is called after a pane produces output; used to schedule a
	// re-render. May be nil in tests.
	onOutput func()
}

func newSession() *session {
	s := &session{windows: map[int]*window{}, panes: map[int]*pane{}}
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
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
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
	br := bufio.NewReaderSize(p.ptmx, 32*1024)
	for {
		if err := p.vt.Parse(br); err != nil {
			p.notifyExit(exitCh)
			return
		}
		if onOutput != nil {
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
	w := &window{index: idx, name: "cogito"}
	p, err := s.spawnPane("", cols, rows)
	if err != nil {
		return nil, err
	}
	w.tree = layout.New(p.id)
	w.activePane = p.id
	s.windows[idx] = w
	s.active = idx
	if n, _ := fgInfo(p); n != "" {
		w.name = n
	} else {
		w.name = fmt.Sprintf("window %d", idx)
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
