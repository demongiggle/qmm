// Package client implements the qmm terminal client: it renders frames
// from the server with tcell and forwards input.
package client

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/qmm/internal/protocol"
)

// debugLog, when QMM_DEBUG_LOG is set, appends a line per event/byte for
// troubleshooting (best effort; failures are ignored).
func debugLog(format string, args ...interface{}) {
	path := os.Getenv("QMM_DEBUG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

// Attach connects to the qmm server at socketPath, renders its screen and
// blocks until the user detaches, the session ends, or the connection breaks.
// The terminal is restored before returning.
func Attach(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("qmm: cannot reach server at %s (%w) — is a qmm session running?", socketPath, err)
	}
	defer conn.Close()

	codec := protocol.NewCodec(conn)

	screen, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("qmm: init screen: %w", err)
	}
	if err := screen.Init(); err != nil {
		return fmt.Errorf("qmm: init screen: %w", err)
	}
	screen.EnableMouse()
	// The caret is a software-rendered cell; hiding the host cursor keeps
	// tcell's per-redraw cursor handling from affecting its blink rate.
	screen.HideCursor()
	// Note: no EnablePaste. Pasted bytes arrive as ordinary key input and are
	// forwarded to the pane, matching how tcell delivers them unencoded.
	screen.Clear()
	screen.Show()

	cols, rows := screen.Size()
	defer screen.Fini() // always restore the terminal

	// Handshake.
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := codec.Send(protocol.Hello{Term: os.Getenv("TERM"), Cols: cols, Rows: rows}); err != nil {
		return fmt.Errorf("qmm: handshake: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msg, err := codec.Recv()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("qmm: handshake: %w", err)
	}
	welcome, ok := msg.(protocol.Welcome)
	if !ok {
		return fmt.Errorf("qmm: unexpected handshake reply from server")
	}

	msgs := make(chan interface{}, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			m, err := codec.Recv()
			if err != nil {
				select {
				case readErr <- err:
				default:
				}
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

	events := make(chan tcell.Event, 64)
	go func() {
		for {
			events <- screen.PollEvent()
		}
	}()

	c := &attach{
		screen:    screen,
		codec:     codec,
		cols:      cols,
		rows:      rows,
		frameCols: welcome.Cols,
		frameRows: welcome.Rows,
	}

	caretTicker := time.NewTicker(caretBlinkInterval)
	defer caretTicker.Stop()

	reason := ""
	for reason == "" {
		select {
		case m, ok := <-msgs:
			if !ok {
				select {
				case err := <-readErr:
					reason = fmt.Sprintf("connection lost: %v", err)
				default:
					reason = "connection lost"
				}
				continue
			}
			switch m := m.(type) {
			case protocol.Frame:
				c.applyFrame(m)
				screen.Show()
			case protocol.Notice:
				screen.Beep()
			case protocol.Goodbye:
				reason = m.Reason
			}
		case <-caretTicker.C:
			if c.blinkCaret() {
				screen.Show()
			}
		case ev := <-events:
			switch ev := ev.(type) {
			case *tcell.EventResize:
				c.cols, c.rows = ev.Size()
				screen.Sync()
				_ = c.codec.Send(protocol.Resize{Cols: c.cols, Rows: c.rows})
			case *tcell.EventKey:
				debugLog("key: key=%v rune=%q mods=%v", ev.Key(), ev.Rune(), ev.Modifiers())
				if b := encodeKey(ev); len(b) > 0 {
					debugLog("send Input: % x", b)
					_ = c.codec.Send(protocol.Input{Data: b})
				}
			case *tcell.EventMouse:
				debugLog("mouse: %v", ev)
				c.handleMouse(ev)
			}
		}
	}

	if reason != "" {
		fmt.Fprintf(os.Stderr, "qmm: %s\n", reason)
	}
	return nil
}
