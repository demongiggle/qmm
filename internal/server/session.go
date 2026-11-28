package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
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
	sb        scrollback
	closeOnce sync.Once
}

// window is a tab inside the session, holding a split tree of panes.
type window struct {
	index      int
	tree       *layout.Tree
	activePane int
	zoomed     bool
	name       string
	// pinnedName keeps an explicit layout name visible instead of letting
	// automatic process-based renaming replace it.
	pinnedName bool
}

// session is one qmm session: a named set of windows.
type session struct {
	name    string
	windows map[int]*window
	active  int
	// last is the window active before the current one (-1 when unknown).
	// prefix+l (last-window) toggles back to it.
	last       int
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
		last:     -1,
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

// setActiveWindow switches the active window and records the previous one so
// last-window (prefix+l) can toggle back. Every window switch goes through
// here: explicit selection, cycling, and new-window creation. The automatic
// fallback after a window's last pane exits keeps its own direct assignment
// because the window it would record as "last" was just deleted.
func (s *session) setActiveWindow(idx int) {
	if _, ok := s.windows[idx]; !ok {
		return
	}
	if idx != s.active {
		s.last = s.active
	}
	s.active = idx
}

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
	if p == nil || p.ptmx == nil {
		return "", ""
	}
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
	return s.spawnPaneCommand(dir, nil, cols, rows)
}

// spawnPaneCommand starts either the user's login shell (nil argv) or a saved
// command (argv) inside a fresh PTY. Commands are exec'd directly so a restored
// pane behaves like any other qmm pane and closes when its command exits.
func (s *session) spawnPaneCommand(dir string, argv []string, cols, rows int) (*pane, error) {
	if len(argv) > 0 && argv[0] == "" {
		return nil, fmt.Errorf("spawn command: empty argv[0]")
	}
	var cmd *exec.Cmd
	if len(argv) == 0 {
		cmd = exec.Command(shellPath(), "-l")
	} else {
		cmd = exec.Command(argv[0], argv[1:]...)
	}
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
		what := "shell"
		if len(argv) > 0 {
			what = "command"
		}
		return nil, fmt.Errorf("spawn %s: %w", what, err)
	}

	id := s.nextPaneID
	s.nextPaneID++

	p := &pane{
		id:   id,
		cmd:  cmd,
		ptmx: ptmx,
	}
	p.initTerminal(cols, rows)
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

func shellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell
}

const (
	maxCommandArgs     = 64
	maxCommandArgBytes = 4096
)

var shellBases = map[string]bool{
	"ash": true, "bash": true, "csh": true, "dash": true, "elvish": true,
	"fish": true, "ksh": true, "mksh": true, "nu": true, "pdksh": true,
	"pwsh": true, "sh": true, "tcsh": true, "xonsh": true, "yash": true,
	"zsh": true,
}

// foregroundProcess inspects the foreground process group of a pane. It returns
// the full argv, the process's current working directory, and whether argv looks
// like an idle login/interactive shell. A shell pane saves no command: restore
// opens a fresh shell in the saved cwd instead.
func foregroundProcess(p *pane) (argv []string, cwd string, isShell bool) {
	if p == nil || p.ptmx == nil {
		return nil, "", false
	}
	pgid, err := tcgetpgrp(p.ptmx.Fd())
	if err != nil || pgid <= 0 {
		return nil, "", false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pgid)); err == nil {
		argv = splitProcCmdline(b)
	}
	if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid)); err == nil {
		cwd = d
	}
	return argv, cwd, isShellArgv(argv)
}

// splitProcCmdline parses /proc/<pid>/cmdline and returns nil when the command
// cannot be persisted safely (invalid UTF-8, too many arguments, or too large).
func splitProcCmdline(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return cleanCommandArgv(parts)
}

func cleanCommandArgv(argv []string) []string {
	if len(argv) == 0 || len(argv) > maxCommandArgs || argv[0] == "" {
		return nil
	}
	total := 0
	for _, arg := range argv {
		if !utf8.ValidString(arg) {
			return nil
		}
		total += len(arg)
		if total > maxCommandArgBytes {
			return nil
		}
	}
	out := make([]string, len(argv))
	copy(out, argv)
	return out
}

func isShellArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := strings.TrimPrefix(filepath.Base(argv[0]), "-")
	if !shellBases[base] {
		return false
	}
	if len(argv) == 1 {
		return true
	}
	// qmm starts login shells with a single -l/--login/interactive flag. A
	// shell running a script or -c command is a real command worth restoring.
	if len(argv) == 2 {
		switch argv[1] {
		case "-l", "--login", "-i", "--interactive":
			return true
		}
	}
	return false
}

// notifyExit reports the pane's death exactly once.
func (p *pane) notifyExit(ch chan int) {
	p.closeOnce.Do(func() {
		if ch != nil {
			ch <- p.id
		}
	})
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

// newWindow creates and activates a fresh window with one pane. Its shell
// starts in the active pane's working directory, matching splits.
func (s *session) newWindow(cols, rows int) (*window, error) {
	idx := s.lowestFreeIndex()
	w := &window{index: idx, name: "qmm"}
	p, err := s.spawnPane(s.activePaneCWD(), cols, rows)
	if err != nil {
		return nil, err
	}
	w.tree = layout.New(p.id)
	w.activePane = p.id
	s.windows[idx] = w
	s.setActiveWindow(idx)
	w.name = fmt.Sprintf("window %d", idx)
	if s.opts().autoNameWindows() {
		if n, _ := fgInfo(p); n != "" {
			w.name = n
		}
	}
	return w, nil
}

// activePane returns the active pane of the active window, or nil.
func (s *session) activePane() *pane {
	w := s.activeWindow()
	if w == nil {
		return nil
	}
	return s.panes[w.activePane]
}

// activePaneCWD returns the working directory of the active pane's foreground
// process, or "" when it cannot be determined. New panes and windows use it so
// they start where the user currently is.
func (s *session) activePaneCWD() string {
	p := s.activePane()
	if p == nil {
		return ""
	}
	_, dir := fgInfo(p)
	return dir
}

// splitActivePane splits the active pane and focuses the new pane. dir is
// layout.Vertical (divider vertical) or layout.Horizontal.
func (s *session) splitActivePane(cols, rows int, dir layout.Direction) (*window, error) {
	w := s.activeWindow()
	if w == nil {
		return nil, fmt.Errorf("no active window")
	}
	// New pane starts in the same working directory as the current one.
	np, err := s.spawnPane(s.activePaneCWD(), cols, rows)
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
