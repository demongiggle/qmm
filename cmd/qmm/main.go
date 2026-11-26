// Command qmm is a terminal multiplexer: panes in windows, multiple
// clients, mouse support. Run `qmm` to start or attach to the session.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/demongiggle/qmm/internal/client"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/server"
	"github.com/demongiggle/qmm/internal/update"
)

// version is derived from the release tag by the Makefile and release builds
// through -ldflags. Untagged builds report "dev" and always offer the latest
// release when qmm update runs.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		if err := ensureServerAndAttach(); err != nil {
			fatal(err)
		}
		return
	}
	switch os.Args[1] {
	case "server":
		// Hidden internal subcommand: run the session daemon in the foreground.
		if err := server.Run(server.Config{}); err != nil {
			fatal(err)
		}
	case "attach":
		sock, err := socketPath()
		if err != nil {
			fatal(err)
		}
		if err := attach(sock); err != nil {
			fatal(err)
		}
	case "kill-server":
		if err := killServer(); err != nil {
			fatal(err)
		}
	case "update":
		if err := runUpdate(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "-h", "--help", "help":
		usage(os.Stdout)
	case "-v", "--version", "version":
		fmt.Printf("qmm %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "qmm: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `qmm %s — a terminal multiplexer

Usage:
  qmm              Start the session server if needed and attach to it
  qmm attach       Attach to the running session (fails if none)
  qmm kill-server  Terminate the session server and all panes
  qmm update       Install the latest qmm release
  qmm --help       Show this help
  qmm --version    Show version

Self-update: qmm update downloads the newest release binary for this platform
and atomically replaces the executable. qmm update --arch arm64 chooses an
architecture and qmm update --pre also considers release candidates. A running
server keeps the previous version until it is restarted.

Inside qmm, press Ctrl-a to send a command prefix, followed by:
  |         split left/right (new pane to the right)
  -         split top/bottom (new pane below)
  c         new window
  n / p     next / previous window
  x         kill current pane
  &         shut down session (confirm with y, cancel with n/Esc)
  o         cycle to next pane
  z         toggle floating zoom (centered, at least 2%% margins)
  Ctrl-← →  resize pane; hold Ctrl to repeat (also: Ctrl-↑ Ctrl-↓)
  d         detach (session keeps running)
  Ctrl-a    send a literal Ctrl-a to the application
  ?         show categorized key help (Esc/q closes)

New panes and windows take focus as soon as they are created.

Mouse: click a pane to focus it, double-click it to toggle floating zoom,
click a window name in the status bar to switch windows, drag a pane border
to resize. Help is scrollable on short terminals with ↑/↓ or PgUp/PgDn.

Config: $XDG_CONFIG_HOME/qmm/config.toml (or ~/.config/qmm/config.toml).
Set QMM_CONFIG to override the full file path; settings changes save
automatically and r / Ctrl-r in Ctrl-a s reloads external edits.
Set QMM_SOCKET_DIR to customize the socket location.
`, version)
}

func socketPath() (string, error) {
	return server.Config{}.SocketPath()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "qmm: %v\n", err)
	os.Exit(1)
}

// runUpdate installs the latest GitHub release over the qmm executable.
func runUpdate(arguments []string, stdout, stderr io.Writer) error {
	err := update.Run(context.Background(), arguments, update.Options{
		CurrentVersion: version,
		Stdout:         stdout,
		Stderr:         stderr,
	})
	// The flag package prints update usage itself for --help.
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

// ensureServerAndAttach starts the daemon if it isn't running, then attaches.
func ensureServerAndAttach() error {
	sock, err := socketPath()
	if err != nil {
		return err
	}
	if !dialable(sock) {
		if err := spawnServer(sock); err != nil {
			return err
		}
		// Wait for the socket to come up.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if dialable(sock) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return attach(sock)
}

func dialable(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func spawnServer(sock string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(sock), "server.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open server log: %w", err)
	}
	cmd := exec.Command(exe, "server")
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("start server: %w", err)
	}
	// The child keeps the log file open; close our copy.
	_ = logf.Close()
	// Reap in background so we don't leave a zombie if it exits immediately.
	go func() { _ = cmd.Wait() }()
	return nil
}

func attach(sock string) error {
	return client.Attach(sock)
}

func killServer() error {
	sock, err := socketPath()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return fmt.Errorf("no qmm server running (socket %s)", sock)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	// The server accepts a bare Shutdown before the Hello handshake.
	if err := codec.Send(protocol.Shutdown{}); err != nil {
		return fmt.Errorf("send shutdown: %w", err)
	}
	fmt.Println("qmm: server shutting down")
	return nil
}
