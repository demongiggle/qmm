package server

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/cogito/internal/protocol"
)

func TestPrefixAmpersandShutdownRequiresConfirmation(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
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

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	defer c.conn.Close()

	// The chord opens a modal dialog but must not end the session yet.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '&'}}); err != nil {
		t.Fatalf("prefix &: %v", err)
	}
	dialog := c.waitForFrame(func(f protocol.Frame) bool {
		text := frameText(f)
		return strings.Contains(text, "SHUTDOWN SESSION") && strings.Contains(text, confirmQuestion)
	})
	if !strings.Contains(frameText(dialog), confirmHint) {
		t.Fatalf("dialog missing answer hint:\n%s", frameText(dialog))
	}
	select {
	case err := <-runErr:
		t.Fatalf("server exited before confirmation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Cancelling returns control to the pane.
	if err := c.codec.Send(protocol.Input{Data: []byte("n")}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && !strings.Contains(frameText(f), confirmQuestion)
	})
	if err := c.codec.Send(protocol.Input{Data: []byte("echo confirm-cancel-marker\n")}); err != nil {
		t.Fatalf("type after cancel: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "confirm-cancel-marker")
	})

	// A second chord followed by y shuts down the server and all clients.
	if err := c.codec.Send(protocol.Input{Data: []byte{0x01, '&'}}); err != nil {
		t.Fatalf("prefix & again: %v", err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), confirmQuestion)
	})
	if err := c.codec.Send(protocol.Input{Data: []byte("y")}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	for {
		msg := c.recv()
		if goodbye, ok := msg.(protocol.Goodbye); ok {
			if goodbye.Reason != "session ended" {
				t.Fatalf("Goodbye reason = %q, want %q", goodbye.Reason, "session ended")
			}
			break
		}
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down after confirmation")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present after shutdown: %v", err)
	}
}
