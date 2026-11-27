package server

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// screenState rebuilds the current screen from full and incremental frames.
// The server coalesces diffs, so a single frame may contain only the cells
// that changed; checking one frame's text is not enough for a stable test.
type screenState struct {
	cols, rows int
	cells      map[[2]int]rune
}

func (st *screenState) apply(f protocol.Frame) string {
	if f.Full || st.cols != f.Cols || st.rows != f.Rows {
		st.cols, st.rows = f.Cols, f.Rows
		st.cells = make(map[[2]int]rune)
	}
	for _, cell := range f.Cells {
		st.cells[[2]int{cell.Row, cell.Col}] = cell.Ch
	}
	var sb strings.Builder
	for y := 0; y < st.rows; y++ {
		for x := 0; x < st.cols; x++ {
			ch, ok := st.cells[[2]int{y, x}]
			if !ok {
				ch = ' '
			}
			sb.WriteRune(ch)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (tc *testClient) waitForScreen(pred func(string) bool) string {
	tc.t.Helper()
	st := &screenState{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text := st.apply(tc.recvFrame())
		if pred(text) {
			return text
		}
	}
	tc.t.Fatal("timed out waiting for matching screen")
	return ""
}

func TestCopyModeScrollbackAndClipboardIntegration(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	t.Cleanup(func() {
		conn, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			return
		}
		_ = protocol.NewCodec(conn).Send(protocol.Shutdown{})
		_ = conn.Close()
	})

	go func() { _ = Run(cfg) }()
	waitSocket(t, sock)
	c := dial(t, sock, 80, 24)
	defer c.conn.Close()

	// Consume the handshake; waitForScreen accumulates the first full frame
	// and all later incremental diffs.
	if _, ok := c.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}

	// Produce more output than the pane can show, so early lines must come
	// from scrollback rather than the live screen.
	cmd := "i=1; while [ $i -le 40 ]; do echo SB-$i; i=$((i+1)); done\n"
	if err := c.codec.Send(protocol.Input{Data: []byte(cmd)}); err != nil {
		t.Fatalf("print lines: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "SB-40")
	})

	// prefix+[ opens the vim-keyed copy mode overlay.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '['}}); err != nil {
		t.Fatalf("prefix [: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "[copy mode]")
	})

	// Vim-style forward search jumps into the scrollback.
	for _, key := range []string{"/", "S", "B", "-", "1", "\r"} {
		if err := c.codec.Send(protocol.Input{Data: []byte(key)}); err != nil {
			t.Fatalf("search key %q: %v", key, err)
		}
	}
	found := c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "SB-1") && strings.Contains(text, "[copy mode]")
	})
	if !strings.Contains(found, "[copy mode]") {
		t.Fatalf("copy mode overlay disappeared during search:\n%s", found)
	}

	// V selects the current line, y copies it to the host clipboard and the
	// session paste buffer, then closes copy mode.
	for _, key := range []string{"V", "y"} {
		if err := c.codec.Send(protocol.Input{Data: []byte(key)}); err != nil {
			t.Fatalf("copy key %q: %v", key, err)
		}
	}
	var clip protocol.Clipboard
	gotClip := false
	for i := 0; i < 50 && !gotClip; i++ {
		if msg, ok := c.recv().(protocol.Clipboard); ok {
			clip, gotClip = msg, true
		}
	}
	if !gotClip {
		t.Fatal("y did not send a Clipboard message")
	}
	if !strings.Contains(clip.Text, "SB-1") {
		t.Fatalf("clipboard = %q, want a line containing SB-1", clip.Text)
	}

	// Input goes back to the pane after copy mode closes.
	if err := c.codec.Send(protocol.Input{Data: []byte("echo AFTER-COPY\n")}); err != nil {
		t.Fatalf("type after copy: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "AFTER-COPY")
	})
}

func TestMouseSelectionCopiesIntoClipboardIntegration(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	t.Cleanup(func() {
		conn, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			return
		}
		_ = protocol.NewCodec(conn).Send(protocol.Shutdown{})
		_ = conn.Close()
	})

	go func() { _ = Run(cfg) }()
	waitSocket(t, sock)
	c := dial(t, sock, 80, 24)
	defer c.conn.Close()

	if _, ok := c.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}

	if err := c.codec.Send(protocol.Input{Data: []byte("echo MOUSESEL-MARKER\n")}); err != nil {
		t.Fatalf("echo marker: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "MOUSESEL-MARKER")
	})

	// Drag across the whole screen: the marker must end up inside the selection.
	for _, msg := range []interface{}{
		protocol.ActionSelectStart{X: 0, Y: 0},
		protocol.ActionSelectUpdate{X: 79, Y: 22},
	} {
		if err := c.codec.Send(msg); err != nil {
			t.Fatalf("select: %v", err)
		}
	}

	// The default copy key (Ctrl-Y) copies the selection and clears it,
	// restoring the live pane view.
	if err := c.codec.Send(protocol.Input{Data: []byte("\x19")}); err != nil {
		t.Fatalf("copy key: %v", err)
	}
	var clip protocol.Clipboard
	gotClip := false
	for i := 0; i < 50 && !gotClip; i++ {
		if msg, ok := c.recv().(protocol.Clipboard); ok {
			clip, gotClip = msg, true
		}
	}
	if !gotClip {
		t.Fatal("copy key did not send a Clipboard message")
	}
	if !strings.Contains(clip.Text, "MOUSESEL-MARKER") {
		t.Fatalf("clipboard = %q, want it to contain MOUSESEL-MARKER", clip.Text)
	}

	// Live mode is restored: the pane receives input again.
	if err := c.codec.Send(protocol.Input{Data: []byte("echo AFTER-MOUSE-SELECT\n")}); err != nil {
		t.Fatalf("type after copy: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "AFTER-MOUSE-SELECT")
	})
}

// TestCopyModeStaysInsideItsPaneIntegration drives a real two-pane session:
// opening copy mode in one pane must not hide the other pane or the status
// bar, which is what a full-screen overlay would do.
func TestCopyModeStaysInsideItsPaneIntegration(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	t.Cleanup(func() {
		conn, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			return
		}
		_ = protocol.NewCodec(conn).Send(protocol.Shutdown{})
		_ = conn.Close()
	})

	go func() { _ = Run(cfg) }()
	waitSocket(t, sock)
	c := dial(t, sock, 80, 24)
	defer c.conn.Close()
	if _, ok := c.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}

	// Marker in the left pane, then split so a second pane takes the focus.
	if err := c.codec.Send(protocol.Input{Data: []byte("echo LEFT-MARK\n")}); err != nil {
		t.Fatalf("echo left: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "LEFT-MARK")
	})
	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Panes) == 2
	})
	if err := c.codec.Send(protocol.Input{Data: []byte("echo RIGHT-MARK\n")}); err != nil {
		t.Fatalf("echo right: %v", err)
	}
	c.waitForScreen(func(text string) bool {
		return strings.Contains(text, "RIGHT-MARK")
	})

	// Entering copy mode forces a full frame, so one frame shows the whole
	// screen: the copy view in the focused pane plus the untouched layout.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '['}}); err != nil {
		t.Fatalf("prefix [: %v", err)
	}
	f := c.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && strings.Contains(frameText(f), "[copy mode]")
	})
	text := frameText(f)
	if !strings.Contains(text, "LEFT-MARK") {
		t.Fatalf("copy mode hid the other pane:\n%s", text)
	}
	var active protocol.PaneRect
	for _, p := range f.Panes {
		if p.Active {
			active = p
		}
	}
	if active.W == 0 || active.W >= f.Cols {
		t.Fatalf("active pane rect = %+v, want one split pane", active)
	}
	if len(f.Tabs) == 0 {
		t.Fatalf("copy mode hid the status bar tabs:\n%s", text)
	}
	// The copy status line sits on the focused pane's bottom row, to the
	// right of the divider -- never across the whole terminal.
	rowText := func(x0, x1, y int) string {
		var sb strings.Builder
		for x := x0; x < x1; x++ {
			sb.WriteRune(cellAt(f, x, y).Ch)
		}
		return sb.String()
	}
	statusY := active.Y + active.H - 1
	if got := rowText(active.X, active.X+active.W, statusY); !strings.Contains(got, "[copy mode]") {
		t.Fatalf("copy status missing from the focused pane row %d: %q", statusY, got)
	}
	if got := rowText(0, active.X, statusY); strings.Contains(got, "[copy mode]") {
		t.Fatalf("copy status painted left of the focused pane: %q", got)
	}
	if got := rowText(0, f.Cols, f.Rows-1); strings.Contains(got, "[copy mode]") {
		t.Fatalf("copy status replaced the session status bar: %q", got)
	}
}
