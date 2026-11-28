package server

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

// Feed the same byte stream as a pane PTY without a shell or socket
// connection. Synchronize on the real pane read loop's render notification.
func newPaneOutputFixture(t *testing.T) (*Server, *clientConn, func(string)) {
	t.Helper()
	s, c := newHelpServer(48, 10)
	s.sess.windows[0].tree = layout.New(1)
	delete(s.sess.panes, 2)
	s.relayout()
	p := s.sess.activePane()
	p.initTerminal(48, 9)
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

func TestPaneOutputCompletesSplitCSIBeforeRenderingAndRecording(t *testing.T) {
	s, c, output := newPaneOutputFixture(t)
	// Each call waits for the input chunk to be parsed and for a frame.
	output("\x1b[1;38;5;")
	requireFrameRow(t, behaviorFrame(t, c), 0, "")
	output("196m歷史\x1b[0m")
	frame := behaviorFrame(t, c)
	requireFrameRow(t, frame, 0, "歷 史")
	if cell := cellAt(frame, 0, 0); cell.FG != 196 || cell.Style&protocol.StyleBold == 0 {
		t.Fatalf("live cell = %+v, want bold text with palette index 196", cell)
	}
	output(strings.Repeat("\r\nnext", 9))
	sendBehaviorInput(s, c, "\x01[", "g", "g")
	frame = behaviorFrame(t, c)
	requireFrameRow(t, frame, 0, "歷 史")
	if cell := cellAt(frame, 2, 0); cell.FG != 196 || cell.Style&protocol.StyleBold == 0 {
		t.Errorf("history cell = %+v, want the original bold text and color", cell)
	}
	sendBehaviorInput(s, c, "y")
	if clip, ok := queuedClipboard(c); !ok || clip.Text != "歷史" {
		t.Fatalf("clipboard from styled history = %#v, want 歷史", clip)
	}
}

func TestPaneOutputShrinkAndGrowKeepsVisibleTailAndExistingHistory(t *testing.T) {
	s, c, output := newPaneOutputFixture(t)
	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 52, Rows: 14}})
	var lines []string
	for i := 1; i <= 13; i++ {
		lines = append(lines, fmt.Sprintf("main-%02d", i))
	}
	lastRow := strings.Repeat("x", 40)
	lines = append(lines, lastRow+"cropped")
	output(strings.Join(lines, "\r\n"))
	requireFrameRow(t, behaviorFrame(t, c), 0, "main-02")
	requireFrameRow(t, behaviorFrame(t, c), 12, lastRow+"cropped")

	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 40, Rows: 10}})
	small := behaviorFrame(t, c)
	requireFrameRow(t, small, 0, "main-06")
	requireFrameRow(t, small, 8, lastRow)
	if small.CursorX != 39 || small.CursorY != 8 || !small.CursorVisible {
		t.Fatalf("cursor after shrink = (%d,%d), visible=%t", small.CursorX, small.CursorY, small.CursorVisible)
	}
	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 52, Rows: 14}})
	grown := behaviorFrame(t, c)
	requireFrameRow(t, grown, 0, "main-06")
	requireFrameRow(t, grown, 8, lastRow)
	requireFrameRow(t, grown, 9, "")
	requireFrameRow(t, grown, 12, "")
	if grown.CursorX != small.CursorX || grown.CursorY != small.CursorY {
		t.Error("growing the pane moved the cursor")
	}

	sendBehaviorInput(s, c, "\x01[", "g", "g")
	frame := behaviorFrame(t, c)
	requireFrameRow(t, frame, 0, "main-01")
	requireFrameRow(t, frame, 1, "main-06")
	if !strings.Contains(frameText(frame), "line 1/14") {
		t.Error("resize created new scrollback entries for the clipped rows")
	}
}

func TestPaneOutputResizesWhileAlternateScreenIsActive(t *testing.T) {
	for _, mode := range []struct {
		decMode     string
		savesCursor bool
	}{
		{"1049", true},
		{"1047", false},
		{"47", false},
	} {
		t.Run(mode.decMode, func(t *testing.T) {
			s, c, output := newPaneOutputFixture(t)
			s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 52, Rows: 14}})
			var lines []string
			for i := 1; i <= 14; i++ {
				lines = append(lines, fmt.Sprintf("main-%02d", i))
			}
			output(strings.Join(lines, "\r\n") + "\x1b[3;7H")
			main := behaviorFrame(t, c)
			output("\x1b[?" + mode.decMode + "h\x1b[Halt-first\r\nalt-second")

			s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 40, Rows: 10}})
			alt := behaviorFrame(t, c)
			requireFrameRow(t, alt, 0, "alt-first")
			requireFrameRow(t, alt, 1, "alt-second")
			output("\x1b[9;1H" + strings.Repeat("alt-scroll\r\n", 3) + "alt-tail")
			s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 50, Rows: 13}})
			alt = behaviorFrame(t, c)
			requireFrameRow(t, alt, 8, "alt-tail")
			requireFrameRow(t, alt, 9, "")

			output("\x1b[?" + mode.decMode + "l")
			restored := behaviorFrame(t, c)
			requireFrameRow(t, restored, 0, "main-02")
			requireFrameRow(t, restored, 8, "main-10")
			requireFrameRow(t, restored, 9, "")
			wantCursor := alt
			if mode.savesCursor {
				wantCursor = main
			}
			if restored.CursorX != wantCursor.CursorX || restored.CursorY != wantCursor.CursorY {
				t.Errorf("cursor after leaving mode %s = (%d,%d), want (%d,%d)", mode.decMode, restored.CursorX, restored.CursorY, wantCursor.CursorX, wantCursor.CursorY)
			}

			sendBehaviorInput(s, c, "\x01[", "g", "g")
			buffer := behaviorFrame(t, c)
			requireFrameRow(t, buffer, 0, "main-01")
			requireFrameRow(t, buffer, 1, "main-02")
			if text := frameText(buffer); !strings.Contains(text, "line 1/13") || strings.Contains(text, "alt-") {
				t.Fatalf("alternate screen affected normal scrollback:\n%s", text)
			}
		})
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
	if cell := cellAt(copyFrame, 2, 0); cell.FG != 9 || cell.Style&protocol.StyleBold == 0 {
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

func TestPaneHistoryFirstScrollAfterShrink(t *testing.T) {
	s, c, output := newPaneOutputFixture(t)
	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 52, Rows: 14}})
	var lines []string
	for i := 1; i <= 14; i++ {
		lines = append(lines, fmt.Sprintf("main-%02d", i))
	}
	output(strings.Join(lines, "\r\n"))
	s.handleClientMessage(clientMsg{c, protocol.Resize{Cols: 40, Rows: 10}})
	requireFrameRow(t, behaviorFrame(t, c), 0, "main-06")
	output("\r\nmain-15")
	requireFrameRow(t, behaviorFrame(t, c), 0, "main-07")
	sendBehaviorInput(s, c, "\x01[", "g", "g")
	requireFrameRow(t, behaviorFrame(t, c), 0, "main-01")
	requireFrameRow(t, behaviorFrame(t, c), 1, "main-06")
}

func TestPaneHistoryBoldColorAcrossReads(t *testing.T) {
	const red = "\x1b[1;31mRED\x1b[0m"
	scroll := strings.Repeat("\r\nnext", 9)
	for name, chunks := range map[string][]string{"one read": {red + scroll}, "two reads": {red, scroll}} {
		t.Run(name, func(t *testing.T) {
			s, c, output := newPaneOutputFixture(t)
			for _, chunk := range chunks {
				output(chunk)
			}
			sendBehaviorInput(s, c, "\x01[", "g", "g")
			frame := behaviorFrame(t, c)
			requireFrameRow(t, frame, 0, "RED")
			if cell := cellAt(frame, 1, 0); cell.FG != 9 || cell.Style&protocol.StyleBold == 0 {
				t.Errorf("history cell = %+v, want VT bold bright red", cell)
			}
		})
	}
}
