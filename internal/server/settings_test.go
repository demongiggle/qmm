package server

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// settingCursorIndex returns the settings-page cursor position of one option,
// so tests keep working when rows are added or reordered.
func settingCursorIndex(t *testing.T, id settingID) int {
	t.Helper()
	cursor := 0
	for _, section := range settingsSections {
		for _, def := range section.Options {
			if def.ID == id {
				return cursor
			}
			cursor++
		}
	}
	t.Fatalf("setting %q is not in the settings page", id)
	return 0
}

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
		"Status bar clock", "Status bar color", "[Slate]",
		"Active pane border", "Cursor shape", "Cursor blink",
		"Pane number timeout", "Double-click zoom", "Keyboard resize step",
		"Automatic window names", "Confirm shutdown", "[On]", "[Green]",
		"[Block]", "[Off]", "[2s]", "[2 cells]", "Copy mode", "r reload", "q close",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings frame missing %q:\n%s", want, text)
		}
	}
	// The page scrolls by one row at 80x24; End reveals the Copy mode options.
	s.handleInput(c, []byte("\x1b[F"))
	text = frameText(*c.frame)
	for _, want := range []string{
		"Copy mode", "Status line color", "[Blue]", "Copy selection key", "[Ctrl-Y]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("scrolled settings frame missing %q:\n%s", want, text)
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
	// The active pane border sits below the status-bar rows.
	s.handleInput(c, []byte("\x1b[B"))
	if want := settingCursorIndex(t, settingBorderColor); c.settingsCursor != want {
		t.Fatalf("cursor = %d, want Active pane border at %d", c.settingsCursor, want)
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
	for _, want := range []string{"Copy mode", "Copy selection key"} {
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

	t.Run("status bar color", func(t *testing.T) {
		s := newTestSession()
		f := (&compositor{}).compose(s, 80, 24, true)
		// Column 60 is empty bar, past the single window tab and before the clock.
		if got := cellAt(f, 60, 23).BG; got != colBarBG {
			t.Fatalf("default status bar BG = %d, want the Slate palette %d", got, colBarBG)
		}
		navy := statusColorIndex(t, "Navy")
		s.opts().setByID(settingStatusBarColor, navy)
		f = (&compositor{}).compose(s, 80, 24, true)
		if got := cellAt(f, 60, 23).BG; got != statusBarColors[navy].bg {
			t.Fatalf("status bar BG = %d, want Navy %d", got, statusBarColors[navy].bg)
		}
		if got := cellAt(f, 60, 23).FG; got != statusBarColors[navy].fg {
			t.Fatalf("status bar FG = %d, want Navy %d", got, statusBarColors[navy].fg)
		}
		// The active tab keeps the border color, whatever the bar color is.
		if got := cellAt(f, 0, 23); got.BG != accentColorCodes[0] || got.FG != colBarActiveFG {
			t.Fatalf("active tab colors = %d/%d, want %d/%d", got.FG, got.BG, colBarActiveFG, accentColorCodes[0])
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
	for c1.settingsCursor < settingCursorIndex(t, settingBorderColor) {
		s.handleInput(c1, []byte("\x1b[B"))
	}
	s.handleInput(c1, []byte("\x1b[C")) // Green -> Cyan, shared value
	if !strings.Contains(frameText(*c2.frame), "Cyan") {
		t.Error("second client's settings page did not see the shared value")
	}
}

// TestStatusBarPaletteIsRichAndConsistent guards the shared status-bar palette:
// both bars pick from it, so names must stay unique and every choice readable.
func TestStatusBarPaletteIsRichAndConsistent(t *testing.T) {
	if len(statusBarNames) != len(statusBarColors) {
		t.Fatalf("palette names = %d, colors = %d", len(statusBarNames), len(statusBarColors))
	}
	if len(statusBarNames) < 10 {
		t.Fatalf("palette has %d choices, want at least 10", len(statusBarNames))
	}
	names := map[string]bool{}
	backgrounds := map[int32]bool{}
	for i, name := range statusBarNames {
		if names[name] {
			t.Errorf("duplicate palette name %q", name)
		}
		names[name] = true
		style := statusBarColors[i]
		if backgrounds[style.bg] {
			t.Errorf("duplicate palette background %d", style.bg)
		}
		backgrounds[style.bg] = true
		if style.bg < 16 || style.bg > 236 {
			t.Errorf("%s background %d is not a dark palette shade", name, style.bg)
		}
		if style.fg < 240 && style.fg != colBarFG {
			t.Errorf("%s text color %d may be unreadable on its background", name, style.fg)
		}
	}
	// Configs written by older builds name these values; keep them valid.
	for _, name := range []string{"Blue", "Purple", "Green", "Amber"} {
		if !names[name] {
			t.Errorf("legacy palette name %q disappeared", name)
		}
	}
}

// TestAccentPaletteIsRichAndConsistent guards the palette shared by the active
// pane border and the active status-bar tab: the tab draws its dark bold label
// straight onto the accent, so every choice must stay bright enough to read.
func TestAccentPaletteIsRichAndConsistent(t *testing.T) {
	if len(accentColorNames) != len(accentColorCodes) {
		t.Fatalf("accent names = %d, codes = %d", len(accentColorNames), len(accentColorCodes))
	}
	if len(accentColorNames) < 10 {
		t.Fatalf("accent palette has %d choices, want at least 10", len(accentColorNames))
	}
	names := map[string]bool{}
	codes := map[int32]bool{}
	for i, name := range accentColorNames {
		if names[name] {
			t.Errorf("duplicate accent name %q", name)
		}
		names[name] = true
		code := accentColorCodes[i]
		if codes[code] {
			t.Errorf("duplicate accent color %d", code)
		}
		codes[code] = true
		if got := xtermLuminance(code); got < 0.15 {
			t.Errorf("%s accent %d is too dark for the dark tab label (luminance %.2f)", name, code, got)
		}
	}
	// Configs written by older builds name these values; keep them valid.
	for _, name := range []string{"Green", "Cyan", "Yellow", "Magenta"} {
		if !names[name] {
			t.Errorf("legacy accent name %q disappeared", name)
		}
	}
	// The active tab pairs the accent with a dark label.
	if colBarActiveFG > 16 {
		t.Errorf("active tab label color %d is not dark; the accent palette assumes it", colBarActiveFG)
	}
}

// xtermLuminance approximates the relative luminance of a 6x6x6-cube xterm-256
// color, which is enough to reject accents that would swallow the tab label.
func xtermLuminance(idx int32) float64 {
	if idx < 16 || idx > 231 {
		return 1
	}
	n := int(idx) - 16
	levels := []int{n / 36, (n / 6) % 6, n % 6}
	lum := 0.0
	weights := []float64{0.2126, 0.7152, 0.0722}
	for i, level := range levels {
		channel := 0.0
		if level > 0 {
			channel = float64(55+level*40) / 255
			channel = math.Pow((channel+0.055)/1.055, 2.4)
		}
		lum += weights[i] * channel
	}
	return lum
}

func settingsRowY(t *testing.T, f protocol.Frame, label string) int {
	t.Helper()
	for y := 0; y < f.Rows; y++ {
		var row strings.Builder
		for x := 0; x < f.Cols; x++ {
			row.WriteRune(cellAt(f, x, y).Ch)
		}
		if strings.Contains(row.String(), label) {
			return y
		}
	}
	t.Fatalf("settings frame is missing the %q row", label)
	return 0
}

// checkValueChip asserts the value chip on one settings row is painted with fg
// and bg, which is how color options preview their palette.
func checkValueChip(t *testing.T, f protocol.Frame, y int, value string, fg, bg int32) {
	t.Helper()
	chip := "[" + value + "]"
	runes := []rune(chip)
	for x := 0; x+len(runes) <= f.Cols; x++ {
		match := true
		for i, r := range runes {
			if cellAt(f, x+i, y).Ch != r {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		cell := cellAt(f, x, y)
		if cell.FG != fg || cell.BG != bg {
			t.Fatalf("%s chip colors = %d/%d, want %d/%d", chip, cell.FG, cell.BG, fg, bg)
		}
		if cell.Style&protocol.StyleBold == 0 {
			t.Errorf("%s chip is not bold", chip)
		}
		return
	}
	t.Fatalf("row %d has no %s chip", y, chip)
}

// TestSettingsColorRowsPreviewTheirPalette keeps the settings page honest: the
// status bar, status line, and active accent options must show the colors they
// select instead of a generic highlight.
func TestSettingsColorRowsPreviewTheirPalette(t *testing.T) {
	s, c := newHelpServer(80, 26)
	opts := s.sess.opts()
	navy := statusColorIndex(t, "Navy")
	olive := statusColorIndex(t, "Olive")
	pink := accentColorIndex(t, "Pink")
	opts.setByID(settingStatusBarColor, navy)
	opts.setByID(settingCopyStatusColor, olive)
	opts.setByID(settingBorderColor, pink)
	fireBinding(s, c, "s")
	// The page holds 20 rows, so scroll to the end to keep the copy-mode color
	// row visible alongside the appearance ones.
	s.handleInput(c, []byte("G"))
	f := *c.frame

	bar := statusBarColors[navy]
	checkValueChip(t, f, settingsRowY(t, f, "Status bar color"), "Navy", bar.fg, bar.bg)
	line := statusBarColors[olive]
	checkValueChip(t, f, settingsRowY(t, f, "Status line color"), "Olive", line.fg, line.bg)
	// The accent previews the active tab: a dark label on the accent color.
	checkValueChip(t, f, settingsRowY(t, f, "Active pane border"), "Pink", colBarActiveFG, accentColorCodes[pink])

	// Cycling the value updates the preview on the selected row.
	opts.setByID(settingBorderColor, 0)    // Green
	opts.setByID(settingStatusBarColor, 0) // back to Slate
	s.handleInput(c, []byte("\x1b[H"))     // Home: the clock is selected
	s.handleInput(c, []byte("\x1b[B"))     // Status bar color
	s.handleInput(c, []byte("\x1b[C"))     // Slate -> Blue
	f = *c.frame
	blue := statusBarColors[1]
	checkValueChip(t, f, settingsRowY(t, f, "Status bar color"), "Blue", blue.fg, blue.bg)
	checkValueChip(t, f, settingsRowY(t, f, "Active pane border"), "Green", colBarActiveFG, accentColorCodes[0])

	// Options that are not colors keep the selected-row highlight.
	s.handleInput(c, []byte("\x1b[B")) // Active pane border
	s.handleInput(c, []byte("\x1b[B")) // Cursor shape
	f = *c.frame
	checkValueChip(t, f, settingsRowY(t, f, "Cursor shape"), "Block", colBarActiveFG, accentColorCodes[0])
}

func accentColorIndex(t *testing.T, name string) int {
	t.Helper()
	for i, n := range accentColorNames {
		if n == name {
			return i
		}
	}
	t.Fatalf("accent %q is not in the palette", name)
	return 0
}
