package server

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestPrefixSOpensCategorizedSettings(t *testing.T) {
	if !isExactBinding("s") {
		t.Fatal("prefix s is not registered")
	}
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "s")
	if !c.settingsOpen {
		t.Fatal("prefix s did not open settings")
	}
	if c.frame == nil {
		t.Fatal("settings binding did not render a frame")
	}
	f := *c.frame
	if !f.Full {
		t.Fatal("settings overlay should force a full repaint")
	}
	text := frameText(f)
	for _, want := range []string{
		"QMM SETTINGS", "Appearance", "Panes", "Layout", "Session",
		"Status bar clock", "Active pane border", "Cursor shape", "Cursor blink",
		"Pane number timeout", "Double-click zoom", "Keyboard resize step",
		"Automatic window names", "Confirm shutdown", "[On]", "[Green]",
		"[Block]", "[Off]", "[2s]", "[2 cells]", "r reload", "q/Esc close",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible || len(f.Panes) != 0 || len(f.Splitters) != 0 || len(f.Tabs) != 0 {
		t.Error("settings overlay should hide session interaction")
	}
	selectedStyled := false
	for _, cell := range f.Cells {
		if cell.BG == colSettingsSelectedBG {
			selectedStyled = true
			break
		}
	}
	if !selectedStyled {
		t.Error("settings frame is missing the selected-row highlight")
	}
}

func TestSettingsNavigationAndAdjustment(t *testing.T) {
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "s")

	s.handleInput(c, []byte("\x1b[B"))
	if c.settingsCursor != 1 {
		t.Fatalf("down arrow cursor = %d, want 1", c.settingsCursor)
	}
	s.handleInput(c, []byte("\x1b[C"))
	if got := s.sess.opts().borderColor(); got != 39 {
		t.Fatalf("right arrow border color = %d, want cyan 39", got)
	}
	if text := frameText(*c.frame); !strings.Contains(text, "Cyan") {
		t.Errorf("settings frame did not show the new value:\n%s", text)
	}
	s.handleInput(c, []byte("\x1b[D"))
	if got := s.sess.opts().borderColor(); got != 46 {
		t.Fatalf("left arrow border color = %d, want green 46", got)
	}

	s.handleInput(c, []byte("\x1b[F")) // End
	if c.settingsCursor != settingsOptionCount()-1 {
		t.Fatalf("End cursor = %d, want %d", c.settingsCursor, settingsOptionCount()-1)
	}
	s.handleInput(c, []byte("\x1b[H")) // Home
	if c.settingsCursor != 0 {
		t.Fatalf("Home cursor = %d, want 0", c.settingsCursor)
	}
	s.handleInput(c, []byte("q"))
	if c.settingsOpen {
		t.Fatal("q did not close settings")
	}
	if text := frameText(*c.frame); strings.Contains(text, "QMM SETTINGS") {
		t.Errorf("settings overlay survived close:\n%s", text)
	}
}

func TestSettingsPageScrollsOnSmallTerminal(t *testing.T) {
	page, maxTop := settingsScrollMetrics(40, 10)
	if page < 1 || maxTop == 0 {
		t.Fatalf("settings metrics at 40x10 = page:%d maxTop:%d, want scrollable", page, maxTop)
	}
	s, c := newHelpServer(40, 10)
	fireBinding(s, c, "s")
	text := frameText(*c.frame)
	for _, want := range []string{"QMM SETTINGS", "↑/↓", "r reload", "q close", "1-"} {
		if !strings.Contains(text, want) {
			t.Errorf("small settings frame missing %q:\n%s", want, text)
		}
	}

	s.handleInput(c, []byte("\x1b[6~")) // Page down
	if c.settingsTop == 0 {
		t.Fatal("page down did not scroll the settings page")
	}
	s.handleInput(c, []byte("G")) // End
	if c.settingsCursor != settingsOptionCount()-1 {
		t.Fatalf("End cursor = %d, want %d", c.settingsCursor, settingsOptionCount()-1)
	}
	if c.settingsTop != maxTop {
		t.Fatalf("End scroll top = %d, want %d", c.settingsTop, maxTop)
	}
	text = frameText(*c.frame)
	for _, want := range []string{"Session", "Confirm shutdown"} {
		if !strings.Contains(text, want) {
			t.Errorf("scrolled settings frame missing %q:\n%s", want, text)
		}
	}
}

func TestSettingsPageSkipsTooSmallTerminal(t *testing.T) {
	f := (&compositor{settingsOpen: true}).compose(newTestSession(), 20, 5, true)
	text := frameText(f)
	if strings.Contains(text, "QMM SETTINGS") {
		t.Errorf("settings page covered the terminal-too-small notice:\n%s", text)
	}
	if !strings.Contains(strings.Join(strings.Fields(text), ""), "terminaltoosmall") {
		t.Errorf("too-small notice missing:\n%s", text)
	}
}

func TestSettingsViewScalesWithFutureOptions(t *testing.T) {
	original := settingsSections
	defer func() { settingsSections = original }()

	extra := make([]settingDef, 20)
	for i := range extra {
		extra[i] = settingDef{
			ID:      settingID(fmt.Sprintf("future_%d", i)),
			Label:   fmt.Sprintf("Future option %d", i),
			Values:  onOffValues,
			Default: 0,
		}
	}
	settingsSections = append(append([]settingsSection{}, original...), settingsSection{
		Title:   "Future",
		Options: extra,
	})

	_, maxTop := settingsScrollMetrics(40, 10)
	if maxTop < 20 {
		t.Fatalf("future options maxTop = %d, want a scrollable view", maxTop)
	}
	s, c := newHelpServer(40, 10)
	s.sess.opts().ensure()
	if _, ok := s.sess.settings.values["future_0"]; !ok {
		t.Error("new setting did not get a default value")
	}
	fireBinding(s, c, "s")
	s.handleInput(c, []byte("G"))
	if c.settingsCursor != settingsOptionCount()-1 {
		t.Fatalf("End cursor = %d, want last future option", c.settingsCursor)
	}
}

func TestSettingsValuesAffectBehavior(t *testing.T) {
	t.Run("status bar clock", func(t *testing.T) {
		on := (&compositor{}).compose(newTestSession(), 80, 24, true)
		if cellAt(on, 72, 23).Ch == ' ' {
			t.Fatal("default status bar is missing the clock")
		}
		s := newTestSession()
		s.opts().setByID(settingStatusClock, 1)
		off := (&compositor{}).compose(s, 80, 24, true)
		if got := cellAt(off, 72, 23).Ch; got != ' ' {
			t.Fatalf("clock hidden cell = %q, want blank", got)
		}
	})

	t.Run("active border color", func(t *testing.T) {
		s := newTestSession()
		s.opts().setByID(settingBorderColor, 1)
		f := (&compositor{}).compose(s, 80, 24, true)
		if got := cellAt(f, 40, 5).FG; got != 39 {
			t.Fatalf("active border FG = %d, want cyan 39", got)
		}
		if got := cellAt(f, 0, 23).BG; got != 39 {
			t.Fatalf("active tab BG = %d, want cyan 39", got)
		}
	})

	t.Run("cursor shape and blink", func(t *testing.T) {
		s := newTestSession()
		f := (&compositor{}).compose(s, 80, 24, true)
		if f.CursorShape != protocol.CursorShapeBlock || f.CursorBlink {
			t.Fatalf("default cursor = shape:%d blink:%v, want steady block", f.CursorShape, f.CursorBlink)
		}
		s.opts().setByID(settingCursorShape, 2)
		s.opts().setByID(settingCursorBlink, 0)
		f = (&compositor{settingsOpen: true}).compose(s, 80, 24, true)
		if f.CursorShape != protocol.CursorShapeBar || !f.CursorBlink {
			t.Fatalf("configured cursor = shape:%d blink:%v, want blinking bar", f.CursorShape, f.CursorBlink)
		}
		if f.CursorVisible {
			t.Error("settings overlay should hide the pane cursor")
		}
	})

	t.Run("pane number timeout", func(t *testing.T) {
		s, c := newHelpServer(80, 24)
		s.sess.opts().setByID(settingPaneNumberTime, 2)
		fireBinding(s, c, "q")
		if remaining := time.Until(c.displayPanesDeadline); remaining <= 4*time.Second {
			t.Fatalf("pane number deadline in %v, want about 5s", remaining)
		}
	})

	t.Run("resize step", func(t *testing.T) {
		s, c := newHelpServer(80, 24)
		s.sess.opts().setByID(settingResizeStep, 2)
		s.broadcast()
		startX := repeatWindowFrameSplitterX(t, c)
		s.handleInput(c, []byte("\x01\x1b[1;5C"))
		if got := repeatWindowFrameSplitterX(t, c); got != startX+4 {
			t.Fatalf("Ctrl-Right with 4-cell step = X:%d, want X:%d", got, startX+4)
		}
	})

	t.Run("double-click zoom", func(t *testing.T) {
		s, c := newHelpServer(80, 24)
		s.sess.opts().setByID(settingDoubleClickZoom, 1)
		w := s.activeWindow()
		s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionTogglePaneZoom{PaneID: 2}})
		if w.zoomed || w.activePane != 1 {
			t.Fatalf("zoom off = zoomed:%v active:%d, want false/1", w.zoomed, w.activePane)
		}
	})

	t.Run("automatic window names", func(t *testing.T) {
		s, _ := newHelpServer(80, 24)
		s.sess.opts().setByID(settingAutoNameWindows, 1)
		w := s.activeWindow()
		w.name = "manual"
		s.refreshWindowNames()
		if w.name != "manual" {
			t.Fatalf("window name = %q, want manual", w.name)
		}
	})

	t.Run("shutdown confirmation", func(t *testing.T) {
		s, c := newHelpServer(80, 24)
		s.sess.opts().setByID(settingConfirmShutdown, 1)
		fireBinding(s, c, "&")
		if !s.shutdownRequested || c.confirmOpen {
			t.Fatalf("confirm off = shutdown:%v dialog:%v, want true/false", s.shutdownRequested, c.confirmOpen)
		}
	})
}

func TestSettingsInputIsSwallowedAndActionsDismiss(t *testing.T) {
	s, c := newHelpServer(80, 24)
	w := s.activeWindow()
	w.activePane = 2
	f, err := os.CreateTemp(t.TempDir(), "pane-input")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f.Close()
	s.sess.activePane().ptmx = f

	fireBinding(s, c, "s")
	s.handleInput(c, []byte("echo should-not-run\n"))
	if !c.settingsOpen {
		t.Fatal("unknown key closed the settings page")
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read pane input: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("unknown key reached pane: %q", raw)
	}

	s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: 0, Y: 0}})
	if c.settingsOpen || w.activePane != 2 {
		t.Fatal("mouse focus leaked through the settings page")
	}
}

func TestSettingsOverlayIsPerClientButValuesAreShared(t *testing.T) {
	s, c1 := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}

	fireBinding(s, c1, "s")
	if c1.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both client frames")
	}
	if !strings.Contains(frameText(*c1.frame), "QMM SETTINGS") {
		t.Error("requesting client is missing the settings page")
	}
	if strings.Contains(frameText(*c2.frame), "QMM SETTINGS") {
		t.Error("settings page leaked to another attached client")
	}

	c2.settingsOpen = true
	c2.forceFull = true
	s.broadcast()
	s.handleInput(c1, []byte("\x1b[B"))
	s.handleInput(c1, []byte("\x1b[C")) // Green -> Cyan, shared value
	if !strings.Contains(frameText(*c2.frame), "Cyan") {
		t.Error("second client's settings page did not see the shared value")
	}
}
