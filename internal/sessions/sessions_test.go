package sessions

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestValidateName(t *testing.T) {
	valid := []string{"default", "1", "work", "dev-1", "my_proj.2", "A1"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		".hidden",
		"-flag",
		"a/b",
		"a b",
		"foo.lock",
		"foo.pid",
		"foo.log",
		"a:b",
		string(make([]byte, 65)),
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) expected error, got nil", name)
		}
	}
}

func TestNextName(t *testing.T) {
	if got := NextName(nil); got != "default" {
		t.Fatalf("NextName(nil) = %q, want default", got)
	}
	if got := NextName([]protocol.SessionInfo{{Name: "default"}}); got != "1" {
		t.Fatalf("NextName([default]) = %q, want 1", got)
	}
	if got := NextName([]protocol.SessionInfo{{Name: "default"}, {Name: "1"}, {Name: "3"}}); got != "2" {
		t.Fatalf("NextName([default, 1, 3]) = %q, want 2", got)
	}
	if got := NextName([]protocol.SessionInfo{{Name: "work"}}); got != "default" {
		t.Fatalf("NextName([work]) = %q, want default", got)
	}
}

func TestListDiscoversLiveAndSkipsStale(t *testing.T) {
	dir := t.TempDir()

	// Create a non-socket file and a stale socket file.
	if err := os.WriteFile(filepath.Join(dir, "default.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	staleSock := filepath.Join(dir, "stale")
	staleLn, err := net.Listen("unix", staleSock)
	if err != nil {
		t.Fatal(err)
	}
	// Close without removing the socket file by using a raw unix socket or
	// simply listening on a second file after closing: Go's net.UnixListener
	// unlinks on Close unless SetUnlinkOnClose(false) is called.
	staleLn.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = staleLn.Close()

	// Start two mock session listeners.
	startMock := func(name string, created int64, wins int) func() {
		sock := filepath.Join(dir, name)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("listen %s: %v", name, err)
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					codec := protocol.NewCodec(c)
					msg, err := codec.Recv()
					if err != nil {
						return
					}
					if _, ok := msg.(protocol.InfoRequest); ok {
						_ = codec.Send(protocol.SessionInfo{
							Name:      name,
							Windows:   wins,
							Panes:     wins,
							Clients:   1,
							CreatedAt: created,
							Version:   "v1.0.0",
							PID:       1234,
						})
					}
				}(conn)
			}
		}()
		return func() { _ = ln.Close() }
	}

	stopB := startMock("work", 200, 2)
	defer stopB()
	stopA := startMock("default", 100, 1)
	defer stopA()

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Name != "default" || got[1].Name != "work" {
		t.Fatalf("order = [%s, %s], want [default, work]", got[0].Name, got[1].Name)
	}
	if got[1].Windows != 2 || got[1].Version != "v1.0.0" {
		t.Fatalf("work info = %+v", got[1])
	}
}

func TestResolveExecutableStripsDeletedSuffix(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "qmm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveExecutable(bin + " (deleted)")
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	if got != bin {
		t.Fatalf("ResolveExecutable = %q, want %q", got, bin)
	}
}

func TestListEmptyOrMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := List(missing)
	if err != nil {
		t.Fatalf("List(missing): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(missing) = %+v, want empty", got)
	}
	// Ensure QueryInfo returns promptly on missing socket.
	start := time.Now()
	if _, err := QueryInfo(filepath.Join(missing, "default"), 50*time.Millisecond); err == nil {
		t.Fatal("expected error for missing socket")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("QueryInfo took too long: %v", time.Since(start))
	}
}
