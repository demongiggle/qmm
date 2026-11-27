package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestTranslateAppCursor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		app  bool
		want string
	}{
		{"home normal", "\x1b[H", false, "\x1b[H"},
		{"home app", "\x1b[H", true, "\x1bOH"},
		{"end normal", "\x1b[F", false, "\x1b[F"},
		{"end app", "\x1b[F", true, "\x1bOF"},
		{"up app", "\x1b[A", true, "\x1bOA"},
		{"down app", "\x1b[B", true, "\x1bOB"},
		{"right app", "\x1b[C", true, "\x1bOC"},
		{"left app", "\x1b[D", true, "\x1bOD"},
		{"up normal", "\x1b[A", false, "\x1b[A"},
		{"ctrl-home app stays CSI", "\x1b[1;5H", true, "\x1b[1;5H"},
		{"ctrl-end app stays CSI", "\x1b[1;5F", true, "\x1b[1;5F"},
		{"ctrl-up app stays CSI", "\x1b[1;5A", true, "\x1b[1;5A"},
		{"shift-home app stays CSI", "\x1b[1;2H", true, "\x1b[1;2H"},
		{"plain text untouched", "hello", true, "hello"},
		{"insert untouched", "\x1b[2~", true, "\x1b[2~"},
		{"delete untouched", "\x1b[3~", true, "\x1b[3~"},
		{"pgup untouched", "\x1b[5~", true, "\x1b[5~"},
		{"empty", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(translateAppCursor([]byte(tc.in), tc.app))
			if got != tc.want {
				t.Fatalf("translateAppCursor(%q, app=%v) = %q, want %q", tc.in, tc.app, got, tc.want)
			}
		})
	}
}

func TestHomeEndForwardingRespectsAppCursorMode(t *testing.T) {
	readPane := func(f *os.File) string {
		t.Helper()
		raw, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("read pane input: %v", err)
		}
		return string(raw)
	}

	// Normal mode: CSI passes through untouched.
	s, c := newHelpServer(80, 24)
	f, err := os.CreateTemp(t.TempDir(), "pane-input")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f.Close()
	s.sess.activePane().ptmx = f
	s.handleInput(c, []byte("\x1b[H"))
	s.handleInput(c, []byte("\x1b[F"))
	s.handleInput(c, []byte("\x1b[A"))
	if got, want := readPane(f), "\x1b[H\x1b[F\x1b[A"; got != want {
		t.Fatalf("normal-mode pane input = %q, want %q", got, want)
	}

	// Application cursor mode (smkx/DECCKM): unmodified cursor keys become SS3.
	s2, c2 := newHelpServer(80, 24)
	f2, err := os.CreateTemp(t.TempDir(), "pane-input-app")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f2.Close()
	s2.sess.activePane().ptmx = f2
	if _, err := s2.sess.activePane().vt.Write([]byte("\x1b[?1h")); err != nil {
		t.Fatalf("enable DECCKM: %v", err)
	}
	if !s2.paneAppCursor() {
		t.Fatal("paneAppCursor = false after DECCKM set, want true")
	}
	s2.handleInput(c2, []byte("\x1b[H"))
	s2.handleInput(c2, []byte("\x1b[F"))
	s2.handleInput(c2, []byte("\x1b[A"))
	s2.handleInput(c2, []byte("\x1b[B"))
	s2.handleInput(c2, []byte("\x1b[C"))
	s2.handleInput(c2, []byte("\x1b[D"))
	if got, want := readPane(f2), "\x1bOH\x1bOF\x1bOA\x1bOB\x1bOC\x1bOD"; got != want {
		t.Fatalf("app-mode pane input = %q, want %q", got, want)
	}

	// Modified keys stay CSI even in application mode.
	s3, c3 := newHelpServer(80, 24)
	f3, err := os.CreateTemp(t.TempDir(), "pane-input-mod")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f3.Close()
	s3.sess.activePane().ptmx = f3
	if _, err := s3.sess.activePane().vt.Write([]byte("\x1b[?1h")); err != nil {
		t.Fatalf("enable DECCKM: %v", err)
	}
	s3.handleInput(c3, []byte("\x1b[1;5H"))
	s3.handleInput(c3, []byte("\x1b[1;5A"))
	if got, want := readPane(f3), "\x1b[1;5H\x1b[1;5A"; got != want {
		t.Fatalf("modified pane input = %q, want %q", got, want)
	}
}
func TestSplitPrefixBindings(t *testing.T) {
	// "|" and "-" replace the tmux-style "%" and "\"" split keys.
	for _, seq := range []string{"|", "-"} {
		if !isExactBinding(seq) {
			t.Errorf("prefix %q is not bound", seq)
		}
	}
	for _, legacy := range []string{"%", "\""} {
		if isExactBinding(legacy) {
			t.Errorf("legacy split key %q is still bound", legacy)
		}
	}
}

func TestTogglePaneZoomActionFocusesAndRestores(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	_, inner := zoomRects(80, 23)

	// A double-click on the inactive pane focuses and floats it.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 2}})
	if !w.zoomed || w.activePane != 2 {
		t.Fatalf("double-click zoom = zoomed:%v active:%d, want true/2", w.zoomed, w.activePane)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("zoomed pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}

	// Double-clicking a different exposed pane moves the overlay to it.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 1}})
	if !w.zoomed || w.activePane != 1 {
		t.Fatalf("double-click switch = zoomed:%v active:%d, want true/1", w.zoomed, w.activePane)
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("switched zoom pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != 39 || rows != 23 {
		t.Fatalf("old zoom pane size = %dx%d, want normal 39x23", cols, rows)
	}

	// Double-clicking the floating pane restores the split.
	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 1}})
	if w.zoomed {
		t.Fatal("double-click on floating pane did not restore the split")
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != 40 || rows != 23 {
		t.Fatalf("restored pane size = %dx%d, want normal 40x23", cols, rows)
	}
}

func TestCyclePaneResizesZoomedOverlay(t *testing.T) {
	s, _ := newHelpServer(80, 24)
	w := s.activeWindow()
	w.zoomed = true
	s.relayout()
	_, inner := zoomRects(80, 23)

	s.cyclePane()
	if w.activePane != 2 {
		t.Fatalf("cycle pane active = %d, want 2", w.activePane)
	}
	if cols, rows := s.sess.panes[2].vt.Size(); cols != inner.W || rows != inner.H {
		t.Fatalf("new zoomed pane size = %dx%d, want %dx%d", cols, rows, inner.W, inner.H)
	}
	if cols, rows := s.sess.panes[1].vt.Size(); cols != 40 || rows != 23 {
		t.Fatalf("old zoomed pane size = %dx%d, want normal 40x23", cols, rows)
	}
}

func repeatWindowFrameSplitterX(t *testing.T, c *clientConn) int {
	t.Helper()
	if c.frame == nil || len(c.frame.Splitters) != 1 {
		t.Fatalf("frame splitters = %+v, want exactly one", c.frame)
	}
	return c.frame.Splitters[0].X
}

func TestPrefixStateMachineRepeatsCtrlArrowResize(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.broadcast()
	startX := repeatWindowFrameSplitterX(t, c)

	// One prefix, then three Ctrl-Right presses. Both key-event layouts are
	// regression-covered: several sequences in one Input and one per Input.
	s.handleInput(c, []byte("\x01"+strings.Repeat("\x1b[1;5C", 2)))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+4 {
		t.Fatalf("two Ctrl-Right presses = X:%d, want X:%d", got, startX+4)
	}
	if !c.repeatArmed {
		t.Fatal("repeatable resize binding did not open a repeat window")
	}

	s.handleInput(c, []byte("\x1b[1;5C"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+6 {
		t.Fatalf("third Ctrl-Right press = X:%d, want X:%d", got, startX+6)
	}

	// A different direction is also repeatable, including the rxvt encoding.
	s.handleInput(c, []byte("\x1b[5D"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+4 {
		t.Fatalf("rxvt Ctrl-Left repeat = X:%d, want X:%d", got, startX+4)
	}

	// A fresh prefix chord still works while the repeat window is active.
	s.handleInput(c, []byte("\x01\x1b[1;5D"))
	if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
		t.Fatalf("prefix Ctrl-Left after repeat = X:%d, want X:%d", got, startX+2)
	}
}

func TestPrefixStateMachineEndsCtrlArrowRepeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endRepeat  func(*clientConn)
		otherInput []byte
	}{
		{
			name:       "other key",
			otherInput: []byte("x"),
		},
		{
			name:       "partial escape sequence",
			otherInput: []byte("\x1b[1;5"),
		},
		{
			name: "timeout",
			endRepeat: func(c *clientConn) {
				c.repeatAt = time.Now().Add(-repeatTimeout - time.Millisecond)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			f, err := os.CreateTemp(t.TempDir(), "pane-input")
			if err != nil {
				t.Fatalf("temp pane input: %v", err)
			}
			defer f.Close()
			s.sess.activePane().ptmx = f
			s.broadcast()
			startX := repeatWindowFrameSplitterX(t, c)

			s.handleInput(c, []byte("\x01\x1b[1;5C"))
			if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
				t.Fatalf("first resize = X:%d, want X:%d", got, startX+2)
			}
			if tc.otherInput != nil {
				s.handleInput(c, tc.otherInput)
			}
			if tc.endRepeat != nil {
				tc.endRepeat(c)
			}

			s.handleInput(c, []byte("\x1b[1;5C"))
			if got := repeatWindowFrameSplitterX(t, c); got != startX+2 {
				t.Fatalf("Ctrl-Right after repeat ended = X:%d, want X:%d", got, startX+2)
			}
			if c.repeatArmed {
				t.Fatal("repeat window stayed active")
			}
			wantRaw := "\x1b[1;5C"
			if tc.otherInput != nil {
				wantRaw = string(tc.otherInput) + wantRaw
			}
			raw, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatalf("read pane input: %v", err)
			}
			if string(raw) != wantRaw {
				t.Fatalf("pane input = %q, want %q", raw, wantRaw)
			}
		})
	}
}
