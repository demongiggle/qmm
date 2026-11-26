package server

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

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

	// Consume the handshake and first full repaint before typing, matching
	// the other integration tests' client setup.
	if _, ok := c.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}
	c.recvFrame()

	// Produce more output than the pane can show, so early lines must come
	// from scrollback rather than the live screen.
	cmd := "i=1; while [ $i -le 40 ]; do echo SB-$i; i=$((i+1)); done\n"
	if err := c.codec.Send(protocol.Input{Data: []byte(cmd)}); err != nil {
		t.Fatalf("print lines: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "SB-40")
	})

	// prefix+[ opens the vim-keyed copy mode overlay.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '['}}); err != nil {
		t.Fatalf("prefix [: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "[copy mode]")
	})

	// Vim-style forward search jumps into the scrollback.
	for _, key := range []string{"/", "S", "B", "-", "1", "\r"} {
		if err := c.codec.Send(protocol.Input{Data: []byte(key)}); err != nil {
			t.Fatalf("search key %q: %v", key, err)
		}
	}
	found := c.waitForFrame(func(f protocol.Frame) bool {
		text := frameText(f)
		return strings.Contains(text, "SB-1") && strings.Contains(text, "[copy mode]")
	})
	if !strings.Contains(frameText(found), "[copy mode]") {
		t.Fatalf("copy mode overlay disappeared during search:\n%s", frameText(found))
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
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "AFTER-COPY")
	})
}
