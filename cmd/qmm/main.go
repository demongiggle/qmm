// Command qmm is a terminal multiplexer: panes in windows, multiple sessions,
// multiple clients, mouse support. Run `qmm` to start or attach to a session.
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
	"strings"
	"syscall"
	"time"

	"github.com/demongiggle/qmm/internal/client"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/server"
	"github.com/demongiggle/qmm/internal/sessions"
	"github.com/demongiggle/qmm/internal/update"
)

// version is derived from the release tag by the Makefile and release builds
// through -ldflags. Untagged builds report "dev" and always offer the latest
// release when qmm update runs.
var version = "dev"

// startupExe captures the clean executable path before any self-update can
// replace the binary on disk (which appends " (deleted)" to /proc/self/exe on
// Linux).
var startupExe string

func main() {
	startupExe, _ = os.Executable()

	if len(os.Args) < 2 {
		if err := ensureDefaultAndAttach(); err != nil {
			fatal(err)
		}
		return
	}
	switch os.Args[1] {
	case "server":
		// Hidden internal subcommand: run one session daemon in the foreground.
		if err := runServer(os.Args[2:]); err != nil {
			fatal(err)
		}
	case "ls":
		if err := runList(os.Args[2:], "", os.Stdout); err != nil {
			fatal(err)
		}
	case "new":
		if err := runNew(os.Args[2:]); err != nil {
			fatal(err)
		}
	case "attach":
		if err := runAttach(os.Args[2:]); err != nil {
			fatal(err)
		}
	case "kill-session":
		if err := runKillSession(os.Args[2:], "", os.Stdout); err != nil {
			fatal(err)
		}
	case "kill-server":
		if err := runKillServer(os.Args[2:], "", os.Stdout); err != nil {
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
  qmm                        Attach to the default (or sole) session, starting it if needed
  qmm ls                     List all running sessions
  qmm new [name]             Start a fresh session (auto-named if omitted) and attach
  qmm attach [name]          Attach to a running session (default if omitted)
  qmm kill-session <name>    Terminate a specific session
  qmm kill-server [name]     Terminate a session (or --all to stop every session)
  qmm update                 Install the latest qmm release
  qmm --help                 Show this help
  qmm --version              Show version

Self-update: qmm update downloads the newest release binary for this platform
and atomically replaces the executable. qmm update --arch arm64 chooses an
architecture and qmm update --pre also considers release candidates. Existing
sessions keep running on the previous binary; launching a new session (from the
CLI or via Ctrl-a S) always starts a fresh daemon from the updated executable.

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
  s         settings
  S         sessions menu (switch or launch a fresh session)
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

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "qmm: %v\n", err)
	os.Exit(1)
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("qmm server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var name string
	fs.StringVar(&name, "session", sessions.DefaultName, "session name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("server does not accept positional arguments")
	}
	return server.Run(server.Config{
		SessionName: name,
		Version:     version,
	})
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

func formatSessionLine(info protocol.SessionInfo) string {
	winWord := "windows"
	if info.Windows == 1 {
		winWord = "window"
	}
	paneWord := "panes"
	if info.Panes == 1 {
		paneWord = "pane"
	}
	details := fmt.Sprintf("%d %s", info.Panes, paneWord)
	if info.Clients > 0 {
		details += fmt.Sprintf(", %d attached", info.Clients)
	}
	line := fmt.Sprintf("%s: %d %s (%s)", info.Name, info.Windows, winWord, details)
	if info.Version != "" {
		line += fmt.Sprintf(" [%s]", info.Version)
	}
	return line
}

func runList(args []string, dirOverride string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("ls does not accept arguments")
	}
	list, err := sessions.List(dirOverride)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no qmm sessions running")
		return nil
	}
	for _, info := range list {
		fmt.Fprintln(stdout, formatSessionLine(info))
	}
	return nil
}

// ensureDefaultAndAttach attaches to "default" if running, otherwise the first
// live session, or starts a fresh "default" session if none are running.
func ensureDefaultAndAttach() error {
	list, err := sessions.List("")
	if err == nil && len(list) > 0 {
		target := list[0].Name
		for _, info := range list {
			if info.Name == sessions.DefaultName {
				target = info.Name
				break
			}
		}
		sock, err := sessions.SocketPath("", target)
		if err != nil {
			return err
		}
		return attach(sock)
	}
	return startAndAttach(sessions.DefaultName)
}

func runNew(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: qmm new [name]")
	}
	var name string
	if len(args) == 1 {
		name = args[0]
		if err := sessions.ValidateName(name); err != nil {
			return err
		}
	} else {
		list, err := sessions.List("")
		if err != nil {
			return err
		}
		name = sessions.NextName(list)
	}
	sock, err := sessions.SocketPath("", name)
	if err != nil {
		return err
	}
	if dialable(sock) {
		return fmt.Errorf("session %q already exists (use `qmm attach %s`)", name, name)
	}
	return startAndAttach(name)
}

func runAttach(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: qmm attach [name]")
	}
	var name string
	if len(args) == 1 {
		name = args[0]
		if err := sessions.ValidateName(name); err != nil {
			return err
		}
	} else {
		var err error
		name, err = resolveDefaultAttachTarget("")
		if err != nil {
			return err
		}
	}
	sock, err := sessions.SocketPath("", name)
	if err != nil {
		return err
	}
	return attach(sock)
}

func resolveDefaultAttachTarget(dirOverride string) (string, error) {
	list, err := sessions.List(dirOverride)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return sessions.DefaultName, nil
	}
	for _, info := range list {
		if info.Name == sessions.DefaultName {
			return sessions.DefaultName, nil
		}
	}
	if len(list) == 1 {
		return list[0].Name, nil
	}
	names := make([]string, len(list))
	for i, info := range list {
		names[i] = info.Name
	}
	return "", fmt.Errorf("multiple sessions running (%s); specify one with `qmm attach <name>`", strings.Join(names, ", "))
}

func startAndAttach(name string) error {
	sock, err := sessions.SocketPath("", name)
	if err != nil {
		return err
	}
	// Check before spawning: a stale QMM_SESSION marker would otherwise start a
	// daemon and only then refuse to attach, leaving an orphan session behind.
	if err := nestedAttachError(sock); err != nil {
		return err
	}
	if !dialable(sock) {
		if err := spawnServer(name, sock); err != nil {
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

func spawnServer(name, sock string) error {
	exe, err := sessions.ResolveExecutable(startupExe)
	if err != nil {
		return err
	}
	logPath := sessions.LogPath(sock, name)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open server log: %w", err)
	}
	cmd := exec.Command(exe, "server", "--session", name)
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

func execArgsForSwitch(req *client.SwitchRequest) []string {
	if req.Create {
		if req.Name != "" {
			return []string{"qmm", "new", req.Name}
		}
		return []string{"qmm", "new"}
	}
	if req.Name != "" {
		return []string{"qmm", "attach", req.Name}
	}
	return []string{"qmm", "attach"}
}

// nestedAttachError refuses to attach to the session whose pane this process
// runs in. Every qmm pane exports QMM_SESSION, so a client started from that
// pane inherits the name of its own session; attaching back to it nests qmm in
// itself: the server's min-of-clients policy then shrinks the session by one
// row per resize round (a pane is always a status row shorter than the
// session), and the client's own drawing becomes that pane's output, so the two
// feed each other until the session collapses to "terminal too small".
//
// The check reads the inherited environment directly, so it does not depend on
// the pane's shell syntax. Set QMM_ALLOW_NESTED to any non-empty value to
// override it, e.g. when a stale marker was carried into a shell that no longer
// feeds the session. Environment-scrubbing launchers (sudo, ssh without
// SendEnv, env -i) drop the marker and therefore bypass the guard; the
// server-side size policy has to stay the safety net for that case.
func nestedAttachError(sock string) error {
	if os.Getenv("QMM_ALLOW_NESTED") != "" {
		return nil
	}
	inside := os.Getenv("QMM_SESSION")
	if inside == "" || inside != filepath.Base(sock) {
		return nil
	}
	return fmt.Errorf("this terminal is a pane of session %q — attaching to it from inside would nest qmm in itself; open a new terminal window, or clear the QMM_SESSION variable (or set QMM_ALLOW_NESTED) to override", inside)
}

func attach(sock string) error {
	if err := nestedAttachError(sock); err != nil {
		return err
	}
	req, err := client.AttachWithSwitch(sock)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	// Re-exec the on-disk qmm binary so both a newly spawned session daemon
	// and this client process run the latest installed executable.
	exe, err := sessions.ResolveExecutable(startupExe)
	if err != nil {
		return fmt.Errorf("switch session: %w", err)
	}
	return syscall.Exec(exe, execArgsForSwitch(req), os.Environ())
}

func runKillSession(args []string, dirOverride string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: qmm kill-session <name>")
	}
	name := args[0]
	if err := sessions.ValidateName(name); err != nil {
		return err
	}
	return killNamedSession(dirOverride, name, stdout)
}

func runKillServer(args []string, dirOverride string, stdout io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: qmm kill-server [name | --all]")
	}
	if len(args) == 1 && (args[0] == "--all" || args[0] == "-a") {
		list, err := sessions.List(dirOverride)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("no qmm sessions running")
		}
		for _, info := range list {
			if err := killNamedSession(dirOverride, info.Name, stdout); err != nil {
				return err
			}
		}
		return nil
	}
	if len(args) == 1 {
		if err := sessions.ValidateName(args[0]); err != nil {
			return err
		}
		return killNamedSession(dirOverride, args[0], stdout)
	}
	name, err := resolveDefaultAttachTarget(dirOverride)
	if err != nil {
		return fmt.Errorf("%v; or use `qmm kill-server --all`", err)
	}
	return killNamedSession(dirOverride, name, stdout)
}

func killNamedSession(dirOverride, name string, stdout io.Writer) error {
	sock, err := sessions.SocketPath(dirOverride, name)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return fmt.Errorf("no qmm session %q running (socket %s)", name, sock)
	}
	defer conn.Close()
	codec := protocol.NewCodec(conn)
	// The server accepts a bare Shutdown before the Hello handshake.
	if err := codec.Send(protocol.Shutdown{}); err != nil {
		return fmt.Errorf("send shutdown: %w", err)
	}
	fmt.Fprintf(stdout, "qmm: session %q shutting down\n", name)
	return nil
}
