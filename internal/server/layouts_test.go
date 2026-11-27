package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
)

func testTwoPaneSpec() layout.Spec {
	tr := layout.New(1)
	tr.Split(1, 2, layout.Vertical)
	return tr.Spec()
}

func TestLayoutsFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layouts.toml")
	list := []savedLayout{
		{
			Name:  "dev",
			Tree:  testTwoPaneSpec(),
			Panes: []savedPane{{CWD: "/tmp", Command: []string{"nvim", "main.go"}}, {CWD: "/work"}},
		},
	}
	if err := writeLayouts(path, list); err != nil {
		t.Fatalf("writeLayouts: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("layouts permissions = %o, want 600", got)
	}
	got, warning, readOnly := loadLayouts(path)
	if warning != nil {
		t.Fatalf("load warning: %+v", warning)
	}
	if readOnly {
		t.Fatal("fresh layouts file is read-only")
	}
	if !reflect.DeepEqual(got, list) {
		t.Fatalf("round trip = %+v, want %+v", got, list)
	}
}

func TestLayoutsMissingFileIsEmpty(t *testing.T) {
	got, warning, readOnly := loadLayouts(filepath.Join(t.TempDir(), "layouts.toml"))
	if warning != nil || readOnly || len(got) != 0 {
		t.Fatalf("missing file = %+v warning=%+v readOnly=%v", got, warning, readOnly)
	}
}

func TestLayoutsMalformedFileIsBackedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layouts.toml")
	original := []byte("this is not toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}
	got, warning, readOnly := loadLayouts(path)
	if warning == nil || warning.BackupPath == "" {
		t.Fatalf("warning = %+v, want backup path", warning)
	}
	if readOnly || len(got) != 0 {
		t.Fatalf("malformed load = %+v readOnly=%v", got, readOnly)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("malformed file still at original path: %v", err)
	}
	backup, err := os.ReadFile(warning.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != string(original) {
		t.Fatalf("backup = %q, want %q", backup, original)
	}
}

func TestLayoutsNewerVersionIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layouts.toml")
	if err := os.WriteFile(path, []byte("version = 9\n"), 0o600); err != nil {
		t.Fatalf("write newer file: %v", err)
	}
	got, warning, readOnly := loadLayouts(path)
	if warning == nil || warning.BackupPath != "" {
		t.Fatalf("warning = %+v, want no backup", warning)
	}
	if !readOnly || len(got) != 0 {
		t.Fatalf("newer load = %+v readOnly=%v, want empty read-only", got, readOnly)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("newer file was moved/removed: %v", err)
	}
}

func TestLayoutsRejectDuplicateNamesAndBadTrees(t *testing.T) {
	cases := []struct {
		name string
		list []savedLayout
	}{
		{
			name: "duplicate names",
			list: []savedLayout{
				{Name: "dev", Tree: testTwoPaneSpec(), Panes: []savedPane{{}, {}}},
				{Name: "dev", Tree: testTwoPaneSpec(), Panes: []savedPane{{}, {}}},
			},
		},
		{
			name: "pane count mismatch",
			list: []savedLayout{
				{Name: "dev", Tree: layout.New(1).Spec(), Panes: []savedPane{{}, {}}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "layouts.toml")
			if err := writeLayouts(path, tc.list); err != nil {
				t.Fatalf("writeLayouts: %v", err)
			}
			got, warning, _ := loadLayouts(path)
			if warning == nil || warning.BackupPath == "" {
				t.Fatalf("warning = %+v, want backup", warning)
			}
			if len(got) != 0 {
				t.Fatalf("invalid layouts loaded: %+v", got)
			}
		})
	}
}

func TestForegroundProcessCapturesCommand(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	p := &pane{id: 1, cmd: cmd, ptmx: ptmx}
	argv, cwd, isShell := foregroundProcess(p)
	if isShell {
		t.Fatal("sleep detected as a shell")
	}
	if len(argv) < 2 || filepath.Base(argv[0]) != "sleep" || argv[1] != "30" {
		t.Fatalf("argv = %v, want sleep 30", argv)
	}
	wantCWD, _ := os.Getwd()
	if cwd != wantCWD {
		t.Fatalf("cwd = %q, want %q", cwd, wantCWD)
	}
}

func TestIsShellArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"/bin/bash"}, true},
		{[]string{"/bin/bash", "-l"}, true},
		{[]string{"-zsh"}, true},
		{[]string{"fish", "--login"}, true},
		{[]string{"/bin/bash", "script.sh"}, false},
		{[]string{"nvim", "main.go"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isShellArgv(tc.argv); got != tc.want {
			t.Fatalf("isShellArgv(%v) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestSaveCurrentLayoutCapturesLiveCommand(t *testing.T) {
	s, _ := newLayoutTestServer(t)
	p, err := s.sess.spawnPaneCommand("", []string{"sleep", "30"}, 80, 23)
	if err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	w := s.sess.windows[0]
	delete(s.sess.panes, w.activePane)
	w.tree = layout.New(p.id)
	w.activePane = p.id

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		argv, _, _ := foregroundProcess(p)
		if len(argv) > 0 && filepath.Base(argv[0]) == "sleep" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, _, err := s.saveCurrentLayout("busy")
	if err != nil {
		t.Fatalf("saveCurrentLayout: %v", err)
	}
	if len(saved.Panes) != 1 {
		t.Fatalf("saved panes = %d, want 1", len(saved.Panes))
	}
	if got := saved.Panes[0].Command; len(got) < 2 || filepath.Base(got[0]) != "sleep" || got[1] != "30" {
		t.Fatalf("saved command = %v, want sleep 30", got)
	}
}

func newLayoutTestServer(t *testing.T) (*Server, *clientConn) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	s, c := newHelpServer(80, 24)
	s.sess = newSession()
	s.sess.name = "default"
	s.sess.windows[0] = &window{index: 0, tree: layout.New(1), activePane: 1, name: "orig"}
	s.sess.panes[1] = &pane{id: 1, vt: vt10x.New(vt10x.WithSize(80, 23))}
	s.sess.active = 0
	s.sess.last = -1
	s.sess.nextPaneID = 2
	s.sess.exitCh = make(chan int, 16)
	s.layoutsPath = filepath.Join(t.TempDir(), "layouts.toml")
	t.Cleanup(func() {
		for _, p := range s.sess.panes {
			if p.ptmx != nil {
				p.kill()
			}
		}
	})
	return s, c
}

func paneContains(p *pane, want string) bool {
	lines, _, _ := p.vtSnapshot()
	var sb strings.Builder
	for _, line := range lines {
		for _, cell := range line.cells {
			if cell.Ch == 0 {
				continue
			}
			sb.WriteRune(cell.Ch)
		}
		sb.WriteByte('\n')
	}
	return strings.Contains(sb.String(), want)
}

func TestRestoreLayoutCreatesNewWindowAndRunsCommands(t *testing.T) {
	s, c := newLayoutTestServer(t)
	l := savedLayout{
		Name: "dev",
		Tree: testTwoPaneSpec(),
		Panes: []savedPane{
			{CWD: s.sess.activePaneCWD()},
			{Command: []string{"/bin/sh", "-c", "printf qmm-restored-marker; sleep 5"}},
		},
	}
	if err := s.restoreLayout(c, l); err != nil {
		t.Fatalf("restoreLayout: %v", err)
	}
	if _, ok := s.sess.windows[0]; !ok {
		t.Fatal("original window was removed")
	}
	w, ok := s.sess.windows[1]
	if !ok {
		t.Fatal("restored window not created")
	}
	if s.sess.active != 1 {
		t.Fatalf("active window = %d, want 1", s.sess.active)
	}
	if w.name != "dev" || !w.pinnedName {
		t.Fatalf("restored window = name:%q pinned:%v, want dev/true", w.name, w.pinnedName)
	}
	ids := w.tree.Panes()
	if len(ids) != 2 {
		t.Fatalf("restored panes = %v, want 2", ids)
	}
	if w.activePane != ids[0] {
		t.Fatalf("active pane = %d, want first pane %d", w.activePane, ids[0])
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p := s.sess.panes[ids[1]]; p != nil && paneContains(p, "qmm-restored-marker") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p := s.sess.panes[ids[1]]; p == nil || !paneContains(p, "qmm-restored-marker") {
		t.Fatal("restored command output did not appear")
	}
	// The explicit layout name must survive automatic process renaming.
	s.refreshWindowNames()
	if w.name != "dev" {
		t.Fatalf("pinned window name = %q, want dev", w.name)
	}
}

func TestRestoreLayoutFallsBackToShellForMissingCommand(t *testing.T) {
	s, c := newLayoutTestServer(t)
	cwd := t.TempDir()
	l := savedLayout{
		Name:  "missing",
		Tree:  layout.New(1).Spec(),
		Panes: []savedPane{{CWD: cwd, Command: []string{"qmm-command-that-does-not-exist"}}},
	}
	if err := s.restoreLayout(c, l); err != nil {
		t.Fatalf("restoreLayout: %v", err)
	}
	w := s.sess.windows[1]
	if w == nil {
		t.Fatal("restored window not created")
	}
	p := s.sess.panes[w.activePane]
	if p == nil || p.cmd == nil {
		t.Fatal("fallback pane missing")
	}
	if got := filepath.Base(p.cmd.Args[0]); got != "sh" {
		t.Fatalf("fallback command = %q, want sh", got)
	}
	if p.cmd.Dir != cwd {
		t.Fatalf("fallback cwd = %q, want %q", p.cmd.Dir, cwd)
	}
	if len(c.queue) == 0 {
		t.Fatal("missing command did not enqueue a notice")
	}
	found := false
	for _, msg := range c.queue {
		if _, ok := msg.(protocol.Notice); ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued messages = %T, want Notice", c.queue)
	}
}

func TestRestoreLayoutRejectsInvalidSpecBeforeSpawning(t *testing.T) {
	s, c := newLayoutTestServer(t)
	l := savedLayout{Name: "bad", Tree: layout.New(1).Spec(), Panes: []savedPane{{}, {}}}
	if err := s.restoreLayout(c, l); err == nil {
		t.Fatal("restoreLayout accepted mismatched pane count")
	}
	if _, ok := s.sess.windows[1]; ok {
		t.Fatal("invalid layout created a window")
	}
}

func TestSaveRestoreDeleteLayoutIntegration(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", filepath.Join(dir, "config.toml"))
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	defer c.conn.Close()
	if _, ok := c.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}
	c.recvFrame()

	// Build a two-pane window, then save it through the layout menu.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '|'}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 })

	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, 'L'}}); err != nil {
		t.Fatalf("open layouts: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return strings.Contains(frameText(f), "QMM LAYOUTS") })
	if err := c.codec.Send(protocol.Input{Data: []byte("s")}); err != nil {
		t.Fatalf("save: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return strings.Contains(frameText(f), "Save layout as:") })
	if err := c.codec.Send(protocol.Input{Data: []byte("dev\r")}); err != nil {
		t.Fatalf("name layout: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		txt := frameText(f)
		return strings.Contains(txt, "Saved layout") && strings.Contains(txt, "dev")
	})
	if err := c.codec.Send(protocol.Input{Data: []byte("q")}); err != nil {
		t.Fatalf("close layouts: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return !strings.Contains(frameText(f), "QMM LAYOUTS") })

	// Reopen and restore into a new window.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, 'L'}}); err != nil {
		t.Fatalf("reopen layouts: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		txt := frameText(f)
		return strings.Contains(txt, "QMM LAYOUTS") && strings.Contains(txt, "dev")
	})
	if err := c.codec.Send(protocol.Input{Data: []byte("\r")}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return !strings.Contains(frameText(f), "QMM LAYOUTS") && len(f.Tabs) >= 2 && len(f.Panes) == 2
	})

	// Delete the saved layout and confirm.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, 'L'}}); err != nil {
		t.Fatalf("reopen layouts for delete: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return strings.Contains(frameText(f), "QMM LAYOUTS") })
	if err := c.codec.Send(protocol.Input{Data: []byte("d")}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return strings.Contains(frameText(f), "Delete layout") })
	if err := c.codec.Send(protocol.Input{Data: []byte("y")}); err != nil {
		t.Fatalf("confirm delete: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return strings.Contains(frameText(f), "Deleted layout") })

	list, warning, readOnly := loadLayouts(filepath.Join(dir, "layouts.toml"))
	if warning != nil || readOnly || len(list) != 0 {
		t.Fatalf("layout file after delete = %+v warning=%+v readOnly=%v", list, warning, readOnly)
	}

	if err := c.codec.Send(protocol.Shutdown{}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestRestoredCommandExitClosesPane(t *testing.T) {
	s, c := newLayoutTestServer(t)
	l := savedLayout{
		Name:  "exit",
		Tree:  layout.New(1).Spec(),
		Panes: []savedPane{{Command: []string{"/bin/sh", "-c", "exit 0"}}},
	}
	if err := s.restoreLayout(c, l); err != nil {
		t.Fatalf("restoreLayout: %v", err)
	}
	w := s.sess.windows[1]
	if w == nil {
		t.Fatal("restored window not created")
	}
	id := w.activePane
	select {
	case got := <-s.sess.exitCh:
		if got != id {
			t.Fatalf("exit pane = %d, want %d", got, id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command exit was not reported")
	}
	s.handlePaneExit(id)
	if _, ok := s.sess.panes[id]; ok {
		t.Fatal("exited pane still in session")
	}
	if _, ok := s.sess.windows[1]; ok {
		t.Fatal("window with no panes still exists")
	}
}

func TestSaveCurrentLayoutPersistsActiveTree(t *testing.T) {
	s, _ := newLayoutTestServer(t)
	saved, existed, err := s.saveCurrentLayout("dev")
	if err != nil {
		t.Fatalf("saveCurrentLayout: %v", err)
	}
	if existed {
		t.Fatal("new layout reported as existing")
	}
	if saved.Name != "dev" {
		t.Fatalf("saved name = %q", saved.Name)
	}
	list, warning, readOnly := loadLayouts(s.layoutsPath)
	if warning != nil || readOnly || len(list) != 1 {
		t.Fatalf("load = %+v warning=%+v readOnly=%v", list, warning, readOnly)
	}
	if !reflect.DeepEqual(list[0].Tree, s.activeWindow().tree.Spec()) {
		t.Fatalf("saved tree = %+v, want %+v", list[0].Tree, s.activeWindow().tree.Spec())
	}

	// Saving the same name replaces it and reports existed.
	if _, existed, err := s.saveCurrentLayout("dev"); err != nil || !existed {
		t.Fatalf("overwrite = existed:%v err:%v, want true/nil", existed, err)
	}
}
