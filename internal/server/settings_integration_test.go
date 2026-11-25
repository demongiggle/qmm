package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/cogito/internal/protocol"
)

func hasStyledCellAt(f protocol.Frame, x, y int, ch rune, fg int32) bool {
	for _, cell := range f.Cells {
		if cell.Col == x && cell.Row == y && cell.Ch == ch && cell.FG == fg {
			return true
		}
	}
	return false
}

// TestSettingsPageViaPrefix opens prefix+s over the wire, changes a value,
// closes the page, and verifies the shared setting changed the rendering.
func TestSettingsPageViaPrefix(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("COGITO_CONFIG", dir+"/config.toml")
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.recvFrame()

	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	split := c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Panes) == 2 && len(f.Splitters) == 1
	})
	splitX := split.Splitters[0].X

	if err := c.codec.Send(protocol.Input{Data: []byte("\x01s")}); err != nil {
		t.Fatalf("prefix s: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		text := frameText(f)
		return f.Full && strings.Contains(text, "COGITO SETTINGS") && strings.Contains(text, "Appearance")
	})

	// Move to "Active pane border" and cycle Green -> Cyan.
	for _, seq := range []string{"\x1b[B", "\x1b[C"} {
		if err := c.codec.Send(protocol.Input{Data: []byte(seq)}); err != nil {
			t.Fatalf("settings key %q: %v", seq, err)
		}
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && strings.Contains(frameText(f), "Cyan")
	})

	if err := c.codec.Send(protocol.Input{Data: []byte("q")}); err != nil {
		t.Fatalf("close settings: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		if !f.Full || strings.Contains(frameText(f), "COGITO SETTINGS") {
			return false
		}
		return hasStyledCellAt(f, splitX, 5, '│', 39)
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
