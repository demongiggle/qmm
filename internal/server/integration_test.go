package server

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

type testClient struct {
	t     *testing.T
	conn  net.Conn
	codec *protocol.Codec
}

func dial(t *testing.T, sock string, cols, rows int) *testClient {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Hello{Term: "xterm-256color", Cols: cols, Rows: rows}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	tc := &testClient{t: t, conn: conn, codec: codec}
	return tc
}

func (tc *testClient) recv() interface{} {
	tc.t.Helper()
	_ = tc.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msg, err := tc.codec.Recv()
	if err != nil {
		tc.t.Fatalf("recv: %v", err)
	}
	return msg
}

func (tc *testClient) recvFrame() protocol.Frame {
	tc.t.Helper()
	for {
		m := tc.recv()
		if f, ok := m.(protocol.Frame); ok {
			return f
		}
	}
}

// waitForFrame accumulates frames until pred is true or the deadline passes.
func (tc *testClient) waitForFrame(pred func(protocol.Frame) bool) protocol.Frame {
	tc.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := tc.recvFrame()
		if pred(f) {
			return f
		}
	}
	tc.t.Fatal("timed out waiting for matching frame")
	return protocol.Frame{}
}

func frameText(f protocol.Frame) string {
	rows := map[int]map[int]rune{}
	maxRow := 0
	for _, c := range f.Cells {
		if rows[c.Row] == nil {
			rows[c.Row] = map[int]rune{}
		}
		rows[c.Row][c.Col] = c.Ch
		if c.Row > maxRow {
			maxRow = c.Row
		}
	}
	var sb strings.Builder
	for r := 0; r <= maxRow; r++ {
		cols := rows[r]
		if cols == nil {
			sb.WriteByte('\n')
			continue
		}
		maxCol := 0
		for c := range cols {
			if c > maxCol {
				maxCol = c
			}
		}
		for c := 0; c <= maxCol; c++ {
			if ch, ok := cols[c]; ok {
				sb.WriteRune(ch)
			} else {
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func waitSocket(t *testing.T, sock string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			if c, err := net.DialTimeout("unix", sock, 100*time.Millisecond); err == nil {
				_ = c.Close()
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server socket did not appear")
}

func TestServerLifecycle(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh") // fast, deterministic shell for marker matching
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()

	// Wait for the server to accept connections.
	waitSocket(t, sock)

	for _, tc := range []struct {
		name  string
		first any
	}{
		{"resize before Hello", protocol.Resize{Cols: 80, Rows: 24}},
		{"zero-width Hello", protocol.Hello{Cols: 0, Rows: 24}},
		{"oversized Hello", protocol.Hello{Cols: 80, Rows: 1001}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("unix", sock, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			codec := protocol.NewCodec(conn)
			if err := codec.Send(tc.first); err != nil {
				t.Fatal(err)
			}
			if msg, err := codec.Recv(); !errors.Is(err, io.EOF) {
				t.Fatalf("invalid handshake returned %#v, %v; want EOF", msg, err)
			}
		})
	}

	c := dial(t, sock, 80, 24)

	// Expect a Welcome first.
	m := c.recv()
	welcome, ok := m.(protocol.Welcome)
	if !ok {
		t.Fatalf("expected Welcome, got %T", m)
	}
	if welcome.Cols != 80 || welcome.Rows != 24 {
		t.Fatalf("welcome = %+v, want 80x24", welcome)
	}

	// First frame should be a full repaint including the status bar.
	f := c.recvFrame()
	if !f.Full {
		t.Fatalf("first frame not Full")
	}
	if text := frameText(f); !strings.Contains(text, "0:") {
		t.Fatalf("status bar missing window entry, got:\n%s", text)
	}

	// Type a marker into the shell; expect it to appear on screen.
	if err := c.codec.Send(protocol.Input{Data: []byte("echo qmm-integration-marker\n")}); err != nil {
		t.Fatalf("send input: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "qmm-integration-marker")
	})

	// Split the pane; expect two panes in the frame metadata.
	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	f = c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 })
	if len(f.Splitters) == 0 {
		t.Fatal("expected at least one splitter after split")
	}

	// New window; expect a tab switch in the frame metadata.
	if err := c.codec.Send(protocol.ActionNewWindow{}); err != nil {
		t.Fatalf("new window: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool { return len(f.Tabs) >= 2 })

	// Kill the server; client should observe goodbye, Run should return nil.
	if err := c.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// New connection to request shutdown.
	c2 := dial(t, sock, 80, 24)
	_ = c2 // server sends Welcome; we only need the shutdown RPC
	if err := c2.codec.Send(protocol.Shutdown{}); err != nil {
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

	// Socket file must be removed.
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present after shutdown")
	}
}

func TestShutdownWithoutHandshake(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	// Send Shutdown as the very first message, like `qmm kill-server`.
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.Shutdown{}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = conn.Close()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down on bare Shutdown")
	}
}

func TestConcurrentServerStartup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	// Two servers racing for the same socket dir: exactly one may win.
	errs := make(chan error, 2)
	go func() { errs <- Run(cfg) }()
	go func() { errs <- Run(cfg) }()

	// Wait until the loser has failed and the winner is accepting.
	var loserErr error
	winnerUp := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !(loserErr != nil && winnerUp) {
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("a server exited cleanly before shutdown was requested")
			}
			if loserErr == nil {
				loserErr = err
			}
		default:
		}
		if c, err := net.DialTimeout("unix", sock, 100*time.Millisecond); err == nil {
			_ = c.Close()
			winnerUp = true
		}
		time.Sleep(25 * time.Millisecond)
	}
	if loserErr == nil {
		t.Fatal("expected one server to lose the startup race, none did")
	}
	t.Logf("loser error: %v", loserErr)
	if !winnerUp {
		t.Fatal("winning server never came up")
	}

	// The winner must be serving: shut it down via bare Shutdown.
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("winner not serving: %v", err)
	}
	if err := protocol.NewCodec(conn).Send(protocol.Shutdown{}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = conn.Close()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("winner returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not shut down")
	}
}

func TestMultiClientMinSize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	a := dial(t, sock, 80, 24)
	_ = a.recv() // Welcome
	// B connects with a larger terminal; effective size stays 80x24.
	b := dial(t, sock, 100, 30)
	m := b.recv()
	w, ok := m.(protocol.Welcome)
	if !ok {
		t.Fatalf("B: expected Welcome, got %T", m)
	}
	if w.Cols != 80 || w.Rows != 24 {
		t.Fatalf("B: effective size %dx%d, want 80x24 (min of clients)", w.Cols, w.Rows)
	}

	// A disconnects; B should grow to its own size on the next full frame.
	if err := a.conn.Close(); err != nil {
		t.Fatalf("close A: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = b.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		msg, err := b.codec.Recv()
		if err != nil {
			t.Fatalf("B recv: %v", err)
		}
		if f, ok := msg.(protocol.Frame); ok && f.Full {
			if f.Cols == 100 && f.Rows == 30 {
				goto done
			}
		}
	}
	t.Fatal("B never received a full frame at 100x30 after A detached")
done:

	if err := b.codec.Send(protocol.Shutdown{}); err != nil {
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

// TestFocusDragAndResize exercises the mouse-driven actions end to end over
// the wire: focus by coordinates, border drag (start/update/end), keyboard
// resize, and window selection by index.
func TestFocusDragAndResize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome

	// Split into two panes; capture the splitter position and active pane.
	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	f := c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 && len(f.Splitters) == 1 })
	splitX := f.Splitters[0].X
	splitterID := f.Splitters[0].ID
	activeBefore := -1
	for _, p := range f.Panes {
		if p.Active {
			activeBefore = p.ID
		}
	}
	if activeBefore == -1 {
		t.Fatal("no active pane reported")
	}

	// Click inside the original left pane: it should become active (splits
	// start out focused on the new right pane).
	if err := c.codec.Send(protocol.ActionFocus{X: 2, Y: 2}); err != nil {
		t.Fatalf("focus: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		for _, p := range f.Panes {
			if p.Active && p.ID != activeBefore {
				return true
			}
		}
		return false
	})

	// Drag the border 4 cells to the right.
	for _, msg := range []interface{}{
		protocol.ActionDragStart{Splitter: splitterID},
		protocol.ActionDragUpdate{Splitter: splitterID, Delta: 4},
		protocol.ActionDragEnd{Splitter: splitterID},
	} {
		if err := c.codec.Send(msg); err != nil {
			t.Fatalf("drag message: %v", err)
		}
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Splitters) == 1 && f.Splitters[0].X == splitX+4
	})

	// Keyboard resize: 2 cells further right.
	if err := c.codec.Send(protocol.ActionResizePane{Direction: protocol.DirRight, Delta: 2}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Splitters) == 1 && f.Splitters[0].X == splitX+6
	})

	// Open a second window and jump to it by index, then back.
	if err := c.codec.Send(protocol.ActionNewWindow{}); err != nil {
		t.Fatalf("new window: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Tabs) == 2 && f.Tabs[1].Active
	})
	if err := c.codec.Send(protocol.ActionSelectWindow{Index: 0}); err != nil {
		t.Fatalf("select window: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		for _, tab := range f.Tabs {
			if tab.Index == 0 {
				return tab.Active
			}
		}
		return false
	})

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

// TestNewPaneAndWindowTakeFocus verifies that keyboard input targets a newly
// created pane or window: splitting activates the new pane, and Ctrl-a c
// activates the new window. Sending "exit" proves focus drives input.
func TestNewPaneAndWindowTakeFocus(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome

	initial := c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 1 })
	originalID := initial.Panes[0].ID
	if !initial.Panes[0].Active {
		t.Fatal("initial pane is not active")
	}

	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	split := c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 })
	createdID, activeID := -1, -1
	activeCount := 0
	for _, p := range split.Panes {
		if p.ID != originalID {
			createdID = p.ID
		}
		if p.Active {
			activeID = p.ID
			activeCount++
		}
	}
	if createdID == -1 {
		t.Fatalf("no new pane in split frame: %+v", split.Panes)
	}
	if activeCount != 1 || activeID != createdID {
		t.Fatalf("active pane = %d (%d active), want new pane %d: %+v",
			activeID, activeCount, createdID, split.Panes)
	}

	// Input follows the new pane: exiting it must leave the original pane.
	if err := c.codec.Send(protocol.Input{Data: []byte("exit\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Panes) == 1 && f.Panes[0].ID == originalID && f.Panes[0].Active
	})

	if err := c.codec.Send(protocol.ActionNewWindow{}); err != nil {
		t.Fatalf("new window: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Tabs) == 2 && f.Tabs[1].Active &&
			len(f.Panes) == 1 && f.Panes[0].Active && f.Panes[0].ID != originalID
	})

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

// TestNewWindowInheritsActivePaneCWD verifies that Ctrl-a c starts the new
// window's shell in the active pane's current working directory.
func TestNewWindowInheritsActivePaneCWD(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 1 })

	work, err := os.MkdirTemp("", "qmm-wd-")
	if err != nil {
		t.Fatalf("temp work dir: %v", err)
	}
	defer os.RemoveAll(work)
	if err := c.codec.Send(protocol.Input{Data: []byte("cd " + work + " && pwd\n")}); err != nil {
		t.Fatalf("cd: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), work)
	})

	// The old window now has this cwd; the new window must start there too.
	if err := c.codec.Send(protocol.ActionNewWindow{}); err != nil {
		t.Fatalf("new window: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Tabs) == 2 && f.Tabs[1].Active && len(f.Panes) == 1
	})

	if err := c.codec.Send(protocol.Input{Data: []byte("pwd\n")}); err != nil {
		t.Fatalf("pwd: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), work)
	})

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

// TestPrefixArrowResize verifies that Ctrl-a Ctrl-Right resizes the active
// pane end to end, and that held Ctrl-Right presses repeat without a prefix
// (byte -> prefix state machine -> tree resize).
func TestPrefixArrowResize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome

	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	f := c.waitForFrame(func(f protocol.Frame) bool { return len(f.Splitters) == 1 })
	startX := f.Splitters[0].X

	// Ctrl-a Ctrl-Right (xterm encoding for Ctrl+RightArrow), followed by
	// two more Ctrl-Right presses without another prefix while the repeat
	// window is active. Sending them back to back avoids timing dependence.
	if err := c.codec.Send(protocol.Input{Data: []byte("\x01\x1b[1;5C")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	if err := c.codec.Send(protocol.Input{Data: []byte("\x1b[1;5C\x1b[1;5C")}); err != nil {
		t.Fatalf("repeat input: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Splitters) == 1 && f.Splitters[0].X == startX+6
	})

	// Ctrl-a Ctrl-Left moves it back one step, even from the repeat window.
	if err := c.codec.Send(protocol.Input{Data: []byte("\x01\x1b[1;5D")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Splitters) == 1 && f.Splitters[0].X == startX+4
	})

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

func TestDetachKeepsSession(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh") // fast, deterministic shell for marker matching
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	// Attach, run a command whose output marker cannot appear in the echoed
	// command line itself, then detach via prefix+d.
	c := dial(t, sock, 80, 24)
	_ = c.recv() // welcome
	_ = c.recvFrame()
	if err := c.codec.Send(protocol.Input{Data: []byte("echo $((100+337))\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	// Wait until the marker is visible on screen before detaching.
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "437")
	})
	// Ctrl-a d
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, 'd'}}); err != nil {
		t.Fatalf("detach: %v", err)
	}

	// Expect Goodbye and server stays alive.
	deadline := time.Now().Add(3 * time.Second)
	gotGoodbye := false
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
		m, err := c.codec.Recv()
		if err != nil {
			break
		}
		if _, ok := m.(protocol.Goodbye); ok {
			gotGoodbye = true
			break
		}
	}
	if !gotGoodbye {
		t.Fatal("did not receive Goodbye on detach")
	}

	// Server should still accept new connections.
	c2 := dial(t, sock, 80, 24)
	_ = c2.recv()
	frame := c2.waitForFrame(func(f protocol.Frame) bool { return f.Full })
	if text := frameText(frame); !strings.Contains(text, "437") {
		t.Fatalf("session content lost after detach (marker 437 not found); screen:\n%s", text)
	}

	// Clean shutdown.
	if err := c2.codec.Send(protocol.Shutdown{}); err != nil {
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

// TestPaneDeathCascadesSessionEnd runs "exit" in the only pane and expects the
// server to notice, tear down the session, and shut down.
func TestPaneDeathCascadesSessionEnd(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh") // fast, deterministic shell for marker matching
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.recvFrame()

	// Tell the shell to exit; the pane's death must cascade:
	// pane -> window -> session -> server exit.
	if err := c.codec.Send(protocol.Input{Data: []byte("exit\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	gotGoodbye := false
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
		m, err := c.codec.Recv()
		if err != nil {
			break
		}
		if _, ok := m.(protocol.Goodbye); ok {
			gotGoodbye = true
			break
		}
	}
	if !gotGoodbye {
		t.Fatal("expected Goodbye when the last pane exits")
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after the last pane died")
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket not removed after shutdown")
	}
}

// TestToggleZoomViaPrefix exercises Ctrl-a z end to end: the active pane
// becomes a centered floating overlay with the zoom flag set, and toggling
// back restores the split.
func TestToggleZoomViaPrefix(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh") // fast, deterministic shell for marker matching
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome

	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	_ = c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 })

	// Ctrl-a z to zoom.
	if err := c.codec.Send(protocol.Input{Data: []byte("\x01z")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	_ = c.waitForFrame(func(f protocol.Frame) bool {
		if !f.Full || len(f.Tabs) == 0 || !f.Tabs[0].Zoomed {
			return false
		}
		overlay, ok := zoomOverlay(f)
		if !ok || len(f.Panes) != 3 || len(f.Splitters) != 0 {
			return false
		}
		// The floating box is inset from the outer edges, not flush with them.
		return overlay.X > 0 && overlay.Y > 0 &&
			hasCell(f, overlay.X, overlay.Y, '┌') &&
			hasCell(f, overlay.X+overlay.W-1, overlay.Y, '┐')
	})

	// Ctrl-a z again to unzoom.
	if err := c.codec.Send(protocol.Input{Data: []byte("\x01z")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		_, zoomed := zoomOverlay(f)
		return len(f.Panes) == 2 && !zoomed && len(f.Tabs) > 0 && !f.Tabs[0].Zoomed
	})

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

// TestTogglePaneZoomViaDoubleClickAction exercises the mouse double-click
// action end to end: the clicked pane becomes the floating overlay, and a
// second double-click on that pane restores the split.
func TestTogglePaneZoomViaDoubleClickAction(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome

	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	split := c.waitForFrame(func(f protocol.Frame) bool { return f.Full && len(f.Panes) == 2 })
	targetID := -1
	for _, p := range split.Panes {
		if !p.Active {
			targetID = p.ID
			break
		}
	}
	if targetID < 0 {
		t.Fatalf("no inactive pane in split frame: %+v", split.Panes)
	}

	if err := c.codec.Send(protocol.ActionTogglePaneZoom{PaneID: targetID}); err != nil {
		t.Fatalf("double-click zoom: %v", err)
	}
	_ = c.waitForFrame(func(f protocol.Frame) bool {
		overlay, ok := zoomOverlay(f)
		return f.Full && ok && overlay.ID == targetID &&
			len(f.Tabs) > 0 && f.Tabs[0].Zoomed &&
			hasCell(f, overlay.X, overlay.Y, '┌')
	})

	if err := c.codec.Send(protocol.ActionTogglePaneZoom{PaneID: targetID}); err != nil {
		t.Fatalf("double-click restore: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		_, zoomed := zoomOverlay(f)
		return f.Full && !zoomed && len(f.Panes) == 2 &&
			len(f.Tabs) > 0 && !f.Tabs[0].Zoomed
	})

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

// TestHelpMenuViaPrefix exercises Ctrl-a ?: the categorized cheatsheet
// appears, q closes it, and normal pane input resumes afterwards.
func TestHelpMenuViaPrefix(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.recvFrame()

	if err := c.codec.Send(protocol.Input{Data: []byte("\x01?")}); err != nil {
		t.Fatalf("prefix ?: %v", err)
	}
	help := c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "QMM HELP")
	})
	for _, want := range []string{"PANES", "WINDOWS", "LAYOUT", "SESSION", "MOUSE"} {
		if !strings.Contains(frameText(help), want) {
			t.Fatalf("help frame missing category %q:\n%s", want, frameText(help))
		}
	}

	if err := c.codec.Send(protocol.Input{Data: []byte("q")}); err != nil {
		t.Fatalf("dismiss help: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && !strings.Contains(frameText(f), "QMM HELP")
	})

	if err := c.codec.Send(protocol.Input{Data: []byte("echo $((400+37))\n")}); err != nil {
		t.Fatalf("input after help: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "437")
	})

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

// TestPrefixSplitBindings drives split keys through the real input stream:
// "|" draws a vertical divider (left/right panes), "-" a horizontal one
// (top/bottom panes).
func TestPrefixSplitBindings(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.recvFrame()

	if err := c.codec.Send(protocol.Input{Data: []byte("\x01|")}); err != nil {
		t.Fatalf("prefix |: %v", err)
	}
	f := c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 2 })
	if len(f.Splitters) != 1 || !f.Splitters[0].Vertical {
		t.Fatalf("prefix | splitters = %+v, want one vertical divider", f.Splitters)
	}

	if err := c.codec.Send(protocol.Input{Data: []byte("\x01-")}); err != nil {
		t.Fatalf("prefix -: %v", err)
	}
	f = c.waitForFrame(func(f protocol.Frame) bool { return len(f.Panes) == 3 })
	horizontal := false
	for _, sp := range f.Splitters {
		if !sp.Vertical {
			horizontal = true
		}
	}
	if !horizontal {
		t.Fatalf("prefix - splitters = %+v, want a horizontal divider", f.Splitters)
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

func zoomOverlay(f protocol.Frame) (protocol.PaneRect, bool) {
	for _, p := range f.Panes {
		if p.Zoomed {
			return p, true
		}
	}
	return protocol.PaneRect{}, false
}

func hasCell(f protocol.Frame, col, row int, ch rune) bool {
	for _, c := range f.Cells {
		if c.Col == col && c.Row == row {
			return c.Ch == ch
		}
	}
	// In a full frame every cell is present; a miss means it does not match.
	return false
}
