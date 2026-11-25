// Command cogito is a terminal multiplexer: panes in windows, multiple
// clients, mouse support. Run `cogito` to start or attach to the session.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/demongiggle/cogito/internal/client"
	"github.com/demongiggle/cogito/internal/protocol"
	"github.com/demongiggle/cogito/internal/server"
)

const version = "0.1.0"

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
	case "-h", "--help", "help":
		usage(os.Stdout)
	case "-v", "--version", "version":
		fmt.Printf("cogito %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "cogito: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `cogito %s — a terminal multiplexer

Usage:
  cogito              Start the session server if needed and attach to it
  cogito attach       Attach to the running session (fails if none)
  cogito kill-server  Terminate the session server and all panes
  cogito --help       Show this help
  cogito --version    Show version

Inside cogito, press Ctrl-a to send a command prefix, followed by:
  %%        split horizontally (new pane to the right)
  "         split vertically (new pane below)
  c         new window
  n / p     next / previous window
  x         kill current pane
  o         cycle to next pane
  z         toggle zoom of current pane (fills the area, 1-cell margin)
  Ctrl-← →  resize pane in direction  (also: Ctrl-↑ Ctrl-↓)
  d         detach (session keeps running)
  Ctrl-a    send a literal Ctrl-a to the application
  ?         show categorized key help (Esc/q closes)

Mouse: click a pane to focus it, click a window name in the status bar to
switch windows, drag a pane border to resize. Help is scrollable on short
terminals with ↑/↓ or PgUp/PgDn.

Config: none. Set COGITO_SOCKET_DIR to customize the socket location.
`, version)
}

func socketPath() (string, error) {
	return server.Config{}.SocketPath()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "cogito: %v\n", err)
	os.Exit(1)
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
		return fmt.Errorf("no cogito server running (socket %s)", sock)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	// The server accepts a bare Shutdown before the Hello handshake.
	if err := codec.Send(protocol.Shutdown{}); err != nil {
		return fmt.Errorf("send shutdown: %w", err)
	}
	fmt.Println("cogito: server shutting down")
	return nil
}
