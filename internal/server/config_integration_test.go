package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/cogito/internal/protocol"
)

func startConfigServer(t *testing.T, configPath string) (string, chan error) {
	t.Helper()
	sockDir := filepath.Join(filepath.Dir(configPath), "socket")
	sock := filepath.Join(sockDir, "default")
	cfg := Config{SocketDir: sockDir}
	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)
	return sock, runErr
}

func shutdownConfigServer(t *testing.T, c *testClient, runErr chan error) {
	t.Helper()
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

func TestConfigRecoveryWarnsOnlyFirstClient(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	t.Setenv("COGITO_CONFIG", configPath)
	broken := "[appearance]\ncursor_shape = \"Triangle\"\n"
	if err := os.WriteFile(configPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}

	sock, runErr := startConfigServer(t, configPath)
	c1 := dial(t, sock, 80, 24)
	_ = c1.recv() // Welcome
	f1 := c1.waitForFrame(func(f protocol.Frame) bool {
		text := frameText(f)
		return f.Full && strings.Contains(text, "CONFIGURATION WARNING") && strings.Contains(text, "Backup:")
	})
	if f1.CursorVisible || len(f1.Panes) != 0 || len(f1.Tabs) != 0 {
		t.Error("startup warning should hide session interaction")
	}
	if err := c1.codec.Send(protocol.Input{Data: []byte("q")}); err != nil {
		t.Fatalf("dismiss warning: %v", err)
	}
	c1.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && !strings.Contains(frameText(f), "CONFIGURATION WARNING")
	})

	c2 := dial(t, sock, 80, 24)
	_ = c2.recv() // Welcome
	f2 := c2.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && !strings.Contains(frameText(f), "CONFIGURATION WARNING")
	})
	if f2.CursorShape != protocol.CursorShapeBlock || f2.CursorBlink {
		t.Fatalf("second client cursor = shape:%d blink:%v, want steady block", f2.CursorShape, f2.CursorBlink)
	}

	backups, err := filepath.Glob(configPath + ".*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup files = %v, %v; want one", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || string(backup) != broken {
		t.Fatalf("backup = %q, %v; want broken config", backup, err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(raw), `cursor_shape = "Block"`) {
		t.Fatalf("default config = %q, %v; want steady block", raw, err)
	}
	logPath := filepath.Join(filepath.Dir(sock), "server.log")
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "backup:") {
		t.Fatalf("server log = %q, %v; want backup path", log, err)
	}

	shutdownConfigServer(t, c2, runErr)
}

func TestConfigReloadViaPrefixAppliesToAllClients(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	t.Setenv("COGITO_CONFIG", configPath)
	sock, runErr := startConfigServer(t, configPath)

	c1 := dial(t, sock, 80, 24)
	_ = c1.recv() // Welcome
	c2 := dial(t, sock, 80, 24)
	_ = c2.recv() // Welcome

	if err := c1.codec.Send(protocol.Input{Data: []byte("\x01s")}); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	c1.waitForFrame(func(f protocol.Frame) bool {
		return f.Full && strings.Contains(frameText(f), "COGITO SETTINGS")
	})

	external := "[appearance]\ncursor_shape = \"Bar\"\ncursor_blink = \"On\"\n"
	if err := os.WriteFile(configPath, []byte(external), 0o600); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	if err := c1.codec.Send(protocol.Input{Data: []byte("r")}); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	f1 := c1.waitForFrame(func(f protocol.Frame) bool {
		return f.CursorShape == protocol.CursorShapeBar && f.CursorBlink &&
			!strings.Contains(frameText(f), "COGITO SETTINGS")
	})
	if strings.Contains(frameText(f1), "CONFIGURATION WARNING") {
		t.Error("successful reload unexpectedly showed a warning")
	}
	f2 := c2.waitForFrame(func(f protocol.Frame) bool {
		return f.CursorShape == protocol.CursorShapeBar && f.CursorBlink
	})
	if strings.Contains(frameText(f2), "COGITO SETTINGS") {
		t.Error("settings page leaked to the second client")
	}

	shutdownConfigServer(t, c2, runErr)
}
