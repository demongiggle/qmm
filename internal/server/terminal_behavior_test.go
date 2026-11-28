package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

// Feed the same byte stream as a pane PTY without a shell, or a socket
// connection. Synchronize on the real pane read loop's render notification.
func newPaneOutputFixture(t *testing.T) (*Server, *clientConn, func(string)) {
	t.Helper()
	s, c := newHelpServer(48, 10)
	s.sess.windows[0].tree = layout.New(1)
	delete(s.sess.panes, 2)
	s.relayout()
	p := s.sess.activePane()
	p.sb = newScrollback(48, 9)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.ptmx = r
	ready := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.readLoop(nil, func() {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
	}()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("pane output reader did not stop")
		}
	})
	return s, c, func(output string) {
		t.Helper()
		if _, err := w.WriteString(output); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ready:
			s.broadcast()
		case <-time.After(3 * time.Second):
			t.Fatal("pane output did not reach the compositor")
		}
	}
}

func requireFrameRow(t *testing.T, frame protocol.Frame, y int, want string) {
	t.Helper()
	var row strings.Builder
	for x := 0; x < frame.Cols; x++ {
		row.WriteRune(cellAt(frame, x, y).Ch)
	}
	if got := strings.TrimRight(row.String(), " "); got != want {
		t.Errorf("frame row %d = %q, want %q", y, got, want)
	}
}

func TestPaneOutputPreservesMainHistoryThroughAlternateScreenAndResize(t *testing.T) {
	s, c, output := newPaneOutputFixture(t)
	output("\x1b[1;31m歷史A\x1b[0m\r\n" + strings.Join([]string{
		"main-two", "main-three", "main-four", "main-five", "main-six",
		"main-seven", "main-eight", "main-nine", "main-ten",
	}, "\r\n"))
	main := behaviorFrame(t, c)
	requireFrameRow(t, main, 0, "main-two")
	requireFrameRow(t, main, 8, "main-ten")

	output("\x1b[?1049h\x1b[H" + strings.Repeat("alternate-output\r\n", 10) + "alternate-last")
	alt := behaviorFrame(t, c)
	requireFrameRow(t, alt, 8, "alternate-last")
	if strings.Contains(frameText(alt), "main-two") {
		t.Fatal("main screen is still visible on the alternate screen")
	}
	output("\x1b[?1049l")
	restored := behaviorFrame(t, c)
	requireFrameRow(t, restored, 0, "main-two")
	requireFrameRow(t, restored, 8, "main-ten")
	if restored.CursorX != main.CursorX || restored.CursorY != main.CursorY {
		t.Error("main-screen cursor moved after leaving the alternate screen")
	}

	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 48, Rows: 12}})
	sendBehaviorInput(s, c, "\x01[", "g", "g")
	copyFrame := behaviorFrame(t, c)
	requireFrameRow(t, copyFrame, 0, "歷 史 A")
	requireFrameRow(t, copyFrame, 1, "main-two")
	if cell := cellAt(copyFrame, 2, 0); cell.FG != 1 || cell.Style&protocol.StyleBold == 0 {
		t.Errorf("historical cell = %+v, want bold red text", cell)
	}
	if strings.Contains(frameText(copyFrame), "alternate-output") {
		t.Fatal("alternate-screen redraws leaked into copy history")
	}
	sendBehaviorInput(s, c, "y")
	if clip, ok := queuedClipboard(c); !ok || clip.Text != "歷史A" {
		t.Fatalf("copied history row = %#v, want 歷史A", clip)
	}
}

func TestPaneOutputScrollRegionAndWideSearchMatchVisibleColumns(t *testing.T) {
	s, c, output := newPaneOutputFixture(t)
	output("HEADER\r\nregion-out\r\nregion-kept\r\n字字hit\r\nFOOTER")
	output("\x1b[2;4r\x1b[4;1H\r\nregion-new")
	frame := behaviorFrame(t, c)
	for row, want := range []string{"HEADER", "region-kept", "字 字 hit", "region-new", "FOOTER"} {
		requireFrameRow(t, frame, row, want)
	}

	sendBehaviorInput(s, c, "\x01[", "g", "g")
	buffer := behaviorFrame(t, c)
	if text := frameText(buffer); strings.Contains(text, "region-out") || !strings.Contains(text, "line 1/9") {
		t.Fatalf("scrolling a region below the top polluted history:\n%s", text)
	}
	sendBehaviorInput(s, c, "/", "hit", "\r")
	text := frameText(behaviorFrame(t, c))
	if !strings.Contains(text, "line 3/9  col 5") {
		t.Fatalf("search did not land after two double-width characters:\n%s", text)
	}
	sendBehaviorInput(s, c, "v", "2", "l", "y")
	if clip, ok := queuedClipboard(c); !ok || clip.Text != "hit" {
		t.Fatalf("copied search match = %#v, want hit", clip)
	}
}

func TestCopyPasteBufferIsSharedButClipboardIsClientLocal(t *testing.T) {
	s, owner := newHelpServer(80, 24)
	peer := newHelpClient()
	s.clients[peer] = struct{}{}
	input := capturePaneInput(t, s.sess.activePane())
	sendBehaviorInput(s, peer, "\x01]")
	if len(peer.queue) != 1 || peer.queue[0] != (protocol.Notice{Text: "qmm: paste buffer is empty"}) {
		t.Fatalf("empty paste response = %#v", peer.queue)
	}
	requirePaneInput(t, input, "")

	writePaneText(t, s, 1, "共享 clipboard")
	sendBehaviorInput(s, owner, "\x01[", "y")
	if clip, ok := queuedClipboard(owner); !ok || clip.Text != "共享 clipboard" {
		t.Fatalf("owner clipboard = %#v", clip)
	}
	if clip, ok := queuedClipboard(peer); ok {
		t.Fatalf("copy also sent a clipboard message to the peer: %#v", clip)
	}
	sendBehaviorInput(s, peer, "\x01]")
	requirePaneInput(t, input, "共享 clipboard")

	// Copying a blank line emits nothing and keeps the previous paste payload.
	sendBehaviorInput(s, owner, "\x01o", "\x01[", "y", "\x01o")
	if len(owner.queue) != 1 {
		t.Fatalf("copying a blank line sent a message: %#v", owner.queue)
	}
	sendBehaviorInput(s, peer, "\x01]")
	requirePaneInput(t, input, "共享 clipboard共享 clipboard")
}
