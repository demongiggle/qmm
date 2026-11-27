package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
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

// TestLastWindowBindingTogglesBack covers prefix+l (tmux last-window): the
// previous window is recorded on every switch, the chord toggles between the
// two windows, and a deleted last window degrades to a no-op.
func TestLastWindowBindingTogglesBack(t *testing.T) {
	if !isExactBinding("l") {
		t.Fatal("prefix l is not registered")
	}
	s, c := newHelpServer(80, 24)
	// A second window with its own pane, the way prefix+c would create it.
	s.sess.windows[1] = &window{index: 1, tree: layout.New(3), activePane: 3, name: "other"}
	s.sess.panes[3] = &pane{id: 3, vt: vt10x.New(vt10x.WithSize(39, 23))}

	// Selecting it records window 0 as the last window.
	s.selectWindow(1)
	if got := (sessionPos{s.sess.active, s.sess.last}); got != (sessionPos{1, 0}) {
		t.Fatalf("after select: active=%d last=%d, want 1/0", got.active, got.last)
	}

	// prefix+l switches back, and pressing it again returns to window 1.
	s.handleInput(c, []byte("\x01l"))
	if got := (sessionPos{s.sess.active, s.sess.last}); got != (sessionPos{0, 1}) {
		t.Fatalf("after last-window: active=%d last=%d, want 0/1", got.active, got.last)
	}
	s.handleInput(c, []byte("\x01l"))
	if got := (sessionPos{s.sess.active, s.sess.last}); got != (sessionPos{1, 0}) {
		t.Fatalf("after second last-window: active=%d last=%d, want 1/0", got.active, got.last)
	}

	// Once the last window is gone the chord is a no-op and clears the
	// stale pointer instead of switching to a dead window.
	delete(s.sess.windows, 0)
	s.handleInput(c, []byte("\x01l"))
	if got := (sessionPos{s.sess.active, s.sess.last}); got != (sessionPos{1, -1}) {
		t.Fatalf("after stale last-window: active=%d last=%d, want 1/-1", got.active, got.last)
	}
}

// sessionPos compares active/last pairs in TestLastWindowBindingTogglesBack.
type sessionPos struct{ active, last int }

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

func TestForceKillPaneWithoutClientKeepsOtherPanes(t *testing.T) {
	s, _ := newHelpServer(80, 24)
	dir := t.TempDir()
	target, err := os.CreateTemp(dir, "target")
	if err != nil {
		t.Fatalf("temp target pty: %v", err)
	}
	other, err := os.CreateTemp(dir, "other")
	if err != nil {
		t.Fatalf("temp other pty: %v", err)
	}
	defer other.Close()
	s.sess.panes[1].ptmx = target
	s.sess.panes[2].ptmx = other

	// The escalation timer for prefix+x sends forceKillPane with no client
	// attached. It used to panic on the nil client and take the server down
	// with every pane two seconds after the close.
	if shutdown := s.handleClientMessage(clientMsg{msg: forceKillPane{paneID: 1}}); shutdown {
		t.Fatal("forceKillPane requested shutdown")
	}
	if _, err := target.Write([]byte("x")); err == nil {
		t.Fatal("target pane's pty is still open")
	}
	if _, err := other.Write([]byte("x")); err != nil {
		t.Fatalf("unrelated pane's pty was closed: %v", err)
	}
	if _, ok := s.sess.panes[1]; !ok {
		t.Fatal("forceKillPane removed the pane instead of waiting for its exit event")
	}

	// A late escalation for a pane that already exited is a harmless no-op.
	if shutdown := s.handleClientMessage(clientMsg{msg: forceKillPane{paneID: 99}}); shutdown {
		t.Fatal("stale forceKillPane requested shutdown")
	}
}
