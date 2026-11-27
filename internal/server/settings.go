package server

import (
	"fmt"
	"time"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	colSettingsSelectedBG int32 = 238
	colSettingsSelectedFG int32 = 255
)

// settingID identifies one session option. String IDs make it easy to add
// future categories without renumbering existing values.
type settingID string

const (
	settingStatusClock     settingID = "status_clock"
	settingBorderColor     settingID = "border_color"
	settingCursorShape     settingID = "cursor_shape"
	settingCursorBlink     settingID = "cursor_blink"
	settingPaneNumberTime  settingID = "pane_number_time"
	settingDoubleClickZoom settingID = "double_click_zoom"
	settingResizeStep      settingID = "resize_step"
	settingAutoNameWindows settingID = "auto_name_windows"
	settingConfirmShutdown settingID = "confirm_shutdown"
	settingStatusBarColor  settingID = "status_bar_color"
	settingCopyStatusColor settingID = "copy_status_color"
	settingCopyKey         settingID = "copy_key"
)

// statusBarStyle is one status-bar palette: a dark line background with light
// text, plus an accent used by the copy-mode label.
type statusBarStyle struct {
	bg     int32 // line background
	fg     int32 // inactive text, clock, hint
	accent int32 // copy-mode label
}

var (
	onOffValues = []string{"On", "Off"}

	// accentColorNames and accentColorCodes color everything that marks the
	// active pane: the border around it and its tab in the session status bar.
	// Every entry is a bright shade so the dark bold tab label stays readable
	// when the accent becomes a background. The first four names are the
	// palette's original choices, and configs saved by older builds may still
	// refer to them. Some names also appear in the status-bar palette below,
	// but there they are dark backgrounds: the settings page previews the chip
	// so a name is never the only cue.
	accentColorNames = []string{
		"Green", "Cyan", "Yellow", "Magenta", "Blue", "Lime",
		"Amber", "Orange", "Red", "Pink", "Purple", "White",
	}
	accentColorCodes = []int32{46, 39, 226, 201, 33, 118, 214, 208, 196, 213, 141, 231}

	// statusBarNames and statusBarColors are shared by the session status bar
	// and the copy-mode status line, so both can be tuned from one palette.
	// Every entry is dark with light text; Slate reproduces the classic grey
	// bar, and Blue is the copy default so the two bars differ out of the box.
	// Names shared with the accent palette above are darker shades here, since
	// these colors are backgrounds rather than highlights.
	statusBarNames = []string{
		"Slate", "Blue", "Navy", "Teal", "Green", "Olive",
		"Amber", "Orange", "Red", "Purple", "Magenta",
	}
	statusBarColors = []statusBarStyle{
		{bg: colBarBG, fg: colBarFG, accent: 46}, // Slate
		{bg: 24, fg: 250, accent: 195},           // Blue
		{bg: 17, fg: 252, accent: 81},            // Navy
		{bg: 30, fg: 252, accent: 159},           // Teal
		{bg: 22, fg: 252, accent: 157},           // Green
		{bg: 58, fg: 252, accent: 191},           // Olive
		{bg: 94, fg: 252, accent: 228},           // Amber
		{bg: 130, fg: 252, accent: 215},          // Orange
		{bg: 52, fg: 252, accent: 210},           // Red
		{bg: 54, fg: 252, accent: 219},           // Purple
		{bg: 88, fg: 252, accent: 213},           // Magenta
	}

	cursorShapeNames = []string{"Block", "Underline", "Bar"}
	cursorShapeCodes = []uint8{
		protocol.CursorShapeBlock,
		protocol.CursorShapeUnderline,
		protocol.CursorShapeBar,
	}

	paneTimeoutNames     = []string{"2s", "1s", "5s"}
	paneTimeoutDurations = []time.Duration{displayPanesTimeout, time.Second, 5 * time.Second}

	resizeStepNames  = []string{"1 cell", "2 cells", "4 cells"}
	resizeStepValues = []int{1, 2, 4}

	// Ctrl-Y is first, and therefore the default: it is a single control byte
	// (0x19) that every terminal forwards untouched, unlike the modified-
	// letter chords terminals commonly grab for their own clipboard. See
	// docs/investigations/terminal-copy-keys-2026.md.
	copyKeyNames = []string{"Ctrl-Y", "Ctrl-Shift-X", "Ctrl-Shift-C", "Ctrl-Insert"}
)

// settingDef is one adjustable row in the settings page. Default is an
// index into Values, so the page and the behavior getters share one source
// of truth.
type settingDef struct {
	ID      settingID
	Label   string
	Values  []string
	Default int
}

type settingsSection struct {
	Title   string
	Options []settingDef
}

var settingsSections = []settingsSection{
	{
		Title: "Appearance",
		Options: []settingDef{
			{ID: settingStatusClock, Label: "Status bar clock", Values: onOffValues, Default: 0},
			{ID: settingStatusBarColor, Label: "Status bar color", Values: statusBarNames, Default: 0},
			{ID: settingBorderColor, Label: "Active pane border", Values: accentColorNames, Default: 0},
			{ID: settingCursorShape, Label: "Cursor shape", Values: cursorShapeNames, Default: 0},
			{ID: settingCursorBlink, Label: "Cursor blink", Values: onOffValues, Default: 1},
		},
	},
	{
		Title: "Panes",
		Options: []settingDef{
			{ID: settingPaneNumberTime, Label: "Pane number timeout", Values: paneTimeoutNames, Default: 0},
			{ID: settingDoubleClickZoom, Label: "Double-click zoom", Values: onOffValues, Default: 0},
		},
	},
	{
		Title: "Layout",
		Options: []settingDef{
			{ID: settingResizeStep, Label: "Keyboard resize step", Values: resizeStepNames, Default: 1},
			{ID: settingAutoNameWindows, Label: "Automatic window names", Values: onOffValues, Default: 0},
		},
	},
	{
		Title: "Session",
		Options: []settingDef{
			{ID: settingConfirmShutdown, Label: "Confirm shutdown", Values: onOffValues, Default: 0},
		},
	},
	{
		Title: "Copy mode",
		Options: []settingDef{
			{ID: settingCopyStatusColor, Label: "Status line color", Values: statusBarNames, Default: 1},
			{ID: settingCopyKey, Label: "Copy selection key", Values: copyKeyNames, Default: 0},
		},
	},
}

// settingsState holds session-wide option values. The view (open, cursor,
// scroll position) lives on each client, but changing a value affects every
// attached client. Values are kept in memory while the server runs and are
// mirrored to the persisted TOML config file.
type settingsState struct {
	values map[settingID]int
}

func defaultSettings() settingsState {
	st := settingsState{values: make(map[settingID]int)}
	for _, section := range settingsSections {
		for _, def := range section.Options {
			st.values[def.ID] = clampSettingDefault(def)
		}
	}
	return st
}

func clampSettingDefault(def settingDef) int {
	if def.Default < 0 || def.Default >= len(def.Values) {
		return 0
	}
	return def.Default
}

// ensure fills in defaults, including options appended after the session
// started. It never overwrites a value the user has already changed.
func (st *settingsState) ensure() {
	if st.values == nil {
		st.values = make(map[settingID]int)
	}
	for id, idx := range defaultSettings().values {
		if _, ok := st.values[id]; !ok {
			st.values[id] = idx
		}
	}
}

// opts returns the session's settings, initializing defaults on first use.
func (s *session) opts() *settingsState {
	if s == nil {
		return &settingsState{}
	}
	s.settings.ensure()
	return &s.settings
}

func settingByID(id settingID) (settingDef, bool) {
	for _, section := range settingsSections {
		for _, def := range section.Options {
			if def.ID == id {
				return def, true
			}
		}
	}
	return settingDef{}, false
}

func (st *settingsState) index(def settingDef) int {
	st.ensure()
	if len(def.Values) == 0 {
		return 0
	}
	idx, ok := st.values[def.ID]
	if !ok {
		idx = clampSettingDefault(def)
	}
	if idx < 0 || idx >= len(def.Values) {
		idx = 0
	}
	return idx
}

func (st *settingsState) setIndex(def settingDef, idx int) {
	st.ensure()
	if len(def.Values) == 0 {
		return
	}
	n := len(def.Values)
	idx = ((idx % n) + n) % n
	st.values[def.ID] = idx
}

func (st *settingsState) adjust(def settingDef, delta int) {
	st.setIndex(def, st.index(def)+delta)
}

func (st *settingsState) text(def settingDef) string {
	if len(def.Values) == 0 {
		return ""
	}
	return def.Values[st.index(def)]
}

func (st *settingsState) setByID(id settingID, idx int) bool {
	def, ok := settingByID(id)
	if !ok {
		return false
	}
	st.setIndex(def, idx)
	return true
}

func (st *settingsState) boolSetting(id settingID, fallback bool) bool {
	def, ok := settingByID(id)
	if !ok {
		return fallback
	}
	return st.index(def) == 0
}

func (st *settingsState) showClock() bool {
	return st.boolSetting(settingStatusClock, true)
}

// borderColor is the accent that marks the active pane: its border, and its
// tab in the session status bar. The default is the accent palette's first
// entry, so both stay in step.
func (st *settingsState) borderColor() int32 {
	def, ok := settingByID(settingBorderColor)
	if !ok {
		return accentColorCodes[0]
	}
	idx := st.index(def)
	if idx < 0 || idx >= len(accentColorCodes) {
		return accentColorCodes[0]
	}
	return accentColorCodes[idx]
}

func (st *settingsState) cursorShape() uint8 {
	def, ok := settingByID(settingCursorShape)
	if !ok {
		return protocol.CursorShapeBlock
	}
	idx := st.index(def)
	if idx < 0 || idx >= len(cursorShapeCodes) {
		return protocol.CursorShapeBlock
	}
	return cursorShapeCodes[idx]
}

func (st *settingsState) cursorBlink() bool {
	def, ok := settingByID(settingCursorBlink)
	if !ok {
		return false
	}
	// Values are On, Off; only the first index blinks.
	return st.index(def) == 0
}

func (st *settingsState) paneNumberTimeoutDuration() time.Duration {
	def, ok := settingByID(settingPaneNumberTime)
	if !ok {
		return displayPanesTimeout
	}
	idx := st.index(def)
	if idx < 0 || idx >= len(paneTimeoutDurations) {
		return displayPanesTimeout
	}
	return paneTimeoutDurations[idx]
}

func (st *settingsState) doubleClickZoom() bool {
	return st.boolSetting(settingDoubleClickZoom, true)
}

func (st *settingsState) resizeStepCells() int {
	def, ok := settingByID(settingResizeStep)
	if !ok {
		return 2
	}
	idx := st.index(def)
	if idx < 0 || idx >= len(resizeStepValues) {
		return 2
	}
	return resizeStepValues[idx]
}

func (st *settingsState) autoNameWindows() bool {
	return st.boolSetting(settingAutoNameWindows, true)
}

func (st *settingsState) confirmShutdown() bool {
	return st.boolSetting(settingConfirmShutdown, true)
}

// statusBarStyleAt returns the palette entry for one status-bar color setting,
// falling back to fallback when the setting is missing or out of range.
func (st *settingsState) statusBarStyleAt(id settingID, fallback int) statusBarStyle {
	def, ok := settingByID(id)
	if !ok {
		return statusBarColors[fallback]
	}
	idx := st.index(def)
	if idx < 0 || idx >= len(statusBarColors) {
		return statusBarColors[fallback]
	}
	return statusBarColors[idx]
}

// statusBarStyle colors the session status bar (tabs and clock).
func (st *settingsState) statusBarStyle() statusBarStyle {
	return st.statusBarStyleAt(settingStatusBarColor, 0)
}

// copyStatusStyle colors the copy-mode status line that covers the session bar.
func (st *settingsState) copyStatusStyle() statusBarStyle {
	return st.statusBarStyleAt(settingCopyStatusColor, 1)
}

// settingPreviewStyle returns the colors that preview one color-setting value
// on its settings row: the bar text/background pair for the two status lines,
// and the dark-on-accent pair the active tab uses. ok is false for options that
// do not pick a color.
func settingPreviewStyle(def settingDef, idx int) (fg, bg int32, ok bool) {
	switch def.ID {
	case settingStatusBarColor, settingCopyStatusColor:
		if idx < 0 || idx >= len(statusBarColors) {
			return 0, 0, false
		}
		style := statusBarColors[idx]
		return style.fg, style.bg, true
	case settingBorderColor:
		if idx < 0 || idx >= len(accentColorCodes) {
			return 0, 0, false
		}
		return colBarActiveFG, accentColorCodes[idx], true
	default:
		return 0, 0, false
	}
}

func settingsOptionCount() int {
	count := 0
	for _, section := range settingsSections {
		count += len(section.Options)
	}
	return count
}

func settingDefAt(cursor int) (settingDef, bool) {
	if cursor < 0 {
		return settingDef{}, false
	}
	for _, section := range settingsSections {
		for _, def := range section.Options {
			if cursor == 0 {
				return def, true
			}
			cursor--
		}
	}
	return settingDef{}, false
}

type settingsLine struct {
	header string
	def    settingDef
	option bool
	cursor int
	blank  bool
}

func settingsLines() []settingsLine {
	lines := make([]settingsLine, 0)
	cursor := 0
	for i, section := range settingsSections {
		lines = append(lines, settingsLine{header: section.Title})
		for _, def := range section.Options {
			lines = append(lines, settingsLine{def: def, option: true, cursor: cursor})
			cursor++
		}
		if i < len(settingsSections)-1 {
			lines = append(lines, settingsLine{blank: true})
		}
	}
	return lines
}

type settingsView struct {
	lines       []settingsLine
	contentRows int
	top         int
	maxTop      int
	scrollable  bool
	lineCount   int
	cursor      int
}

func buildSettingsView(cols, rows, cursor, top int) settingsView {
	lines := settingsLines()
	optionCount := 0
	for _, line := range lines {
		if line.option {
			optionCount++
		}
	}
	cursor = max(0, min(cursor, max(0, optionCount-1)))
	selectedLine := 0
	for i, line := range lines {
		if line.option && line.cursor == cursor {
			selectedLine = i
			break
		}
	}
	contentRows := helpContentRows(cols, rows)
	maxTop := max(0, len(lines)-contentRows)
	if selectedLine < top {
		top = selectedLine
	}
	if selectedLine >= top+contentRows {
		top = selectedLine - contentRows + 1
	}
	top = max(0, min(top, maxTop))
	return settingsView{
		lines:       lines,
		contentRows: contentRows,
		top:         top,
		maxTop:      maxTop,
		scrollable:  maxTop > 0,
		lineCount:   len(lines),
		cursor:      cursor,
	}
}

func settingsScrollMetrics(cols, rows int) (page, maxTop int) {
	v := buildSettingsView(cols, rows, 0, 0)
	return max(1, v.contentRows), v.maxTop
}

func (s *Server) settingsSyncView(c *clientConn) settingsView {
	v := buildSettingsView(s.cols, s.rows, c.settingsCursor, c.settingsTop)
	c.settingsCursor, c.settingsTop = v.cursor, v.top
	return v
}

// openSettings shows the categorized settings page on one client. Like help
// and confirmation, the view is local; option values are session-wide.
func (s *Server) openSettings(c *clientConn) {
	c.helpOpen = false
	c.helpTop = 0
	c.confirmOpen = false
	c.displayPanesOpen = false
	c.displayPanesInput = ""
	c.displayPanesDeadline = time.Time{}
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.settingsOpen = true
	c.settingsCursor = 0
	c.settingsTop = 0
	s.settingsSyncView(c)
	c.forceFull = true
	s.sess.opts().ensure()
	s.broadcast()
}

func (s *Server) closeSettings(c *clientConn) {
	if !c.settingsOpen {
		return
	}
	c.settingsOpen = false
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.forceFull = true
	s.broadcast()
}

// handleSettingsInput consumes keys while the settings page is open.
// Navigation follows the help menu; left/right, Enter, Space and Tab change
// the selected option. r/Ctrl-r reloads the config file. Unknown keys are
// swallowed, not sent to a pane.
func (s *Server) handleSettingsInput(c *clientConn, data []byte) {
	seq := string(data)
	view := buildSettingsView(s.cols, s.rows, c.settingsCursor, c.settingsTop)
	switch {
	case isHelpNav(seq, "A", "k"):
		s.settingsMoveCursor(c, -1)
	case isHelpNav(seq, "B", "j"):
		s.settingsMoveCursor(c, 1)
	case isHelpNav(seq, "5~", "b"), seq == "\x02":
		s.settingsMoveCursor(c, -view.contentRows)
	case isHelpNav(seq, "6~", "f"), seq == "\x06":
		s.settingsMoveCursor(c, view.contentRows)
	case isHelpNav(seq, "H", "g"):
		s.settingsMoveCursor(c, -settingsOptionCount())
	case isHelpNav(seq, "F", "G"):
		s.settingsMoveCursor(c, settingsOptionCount())
	case isHelpNav(seq, "C", "l"), seq == "\t", seq == "\r", seq == "\n", seq == " ":
		s.settingsAdjust(c, 1)
	case isHelpNav(seq, "D", "h"), seq == "\x1b[Z":
		s.settingsAdjust(c, -1)
	case seq == "r", seq == "R", seq == "\x12":
		s.reloadConfig(c)
	case seq == "\x1b", seq == "\x03", seq == "q", seq == "Q", seq == "s", seq == "S", seq == "\x01s":
		s.closeSettings(c)
	default:
		// Keep the page open; a stray key must not reach the shell.
	}
}

func (s *Server) settingsMoveCursor(c *clientConn, delta int) {
	count := settingsOptionCount()
	if count == 0 {
		return
	}
	c.settingsCursor = max(0, min(c.settingsCursor+delta, count-1))
	s.settingsSyncView(c)
	c.forceFull = true
	s.broadcast()
}

func (s *Server) settingsAdjust(c *clientConn, delta int) {
	def, ok := settingDefAt(c.settingsCursor)
	if !ok {
		return
	}
	s.sess.opts().adjust(def, delta)
	s.persistSettings(c)
	s.settingsSyncView(c)
	// Chrome-wide settings (clock, colors, tab width) need a full repaint.
	s.needFull = true
	c.forceFull = true
	s.broadcast()
}

func settingsFooter(view settingsView) string {
	if view.scrollable {
		first := view.top + 1
		last := min(view.lineCount, view.top+view.contentRows)
		return fmt.Sprintf(" ↑/↓ ←/→ r reload %d-%d/%d q close ", first, last, view.lineCount)
	}
	return " ↑/↓ ←/→/Enter r reload q/Esc close "
}

// drawSettings renders an opaque, categorized, scrollable settings page.
func (c *compositor) drawSettings(frame *protocol.Frame, setCell helpSetCell, cols, rows int, opts *settingsState) {
	if cols < minCols || rows < minRows {
		return
	}
	if opts == nil {
		opts = &settingsState{}
	}
	view := buildSettingsView(cols, rows, c.settingsCursor, c.settingsTop)
	c.settingsCursor, c.settingsTop = view.cursor, view.top
	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0
	frame.Panes = nil
	frame.Splitters = nil
	frame.Tabs = nil

	for y := 0; y < max(0, rows-statusRows); y++ {
		for x := 0; x < cols; x++ {
			setCell(x, y, ' ', colHelpMuted, colHelpBackdrop, 0)
		}
	}

	x0, y0, x1, y1 := helpBox(cols, rows)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			setCell(x, y, ' ', colHelpFG, colHelpPanelBG, 0)
		}
	}
	drawHelpFrame(setCell, x0, y0, x1, y1)

	titleWidth := max(0, x1-x0-3)
	putHelpText(setCell, x0+2, y0, titleWidth, " QMM SETTINGS · PREFIX: Ctrl-a ", colHelpTitle, colHelpPanelBG, protocol.StyleBold)
	putHelpText(setCell, x0+2, y1, titleWidth, fitHelpText(settingsFooter(view), titleWidth), colHelpMuted, colHelpPanelBG, 0)

	innerX := x0 + 2
	innerRight := x1 - 2
	width := max(1, innerRight-innerX+1)
	scrollX := -1
	if view.scrollable {
		width = max(1, width-1)
		scrollX = innerRight
		drawHelpScrollbar(setCell, scrollX, y0+1, view.contentRows, helpView{
			contentRows: view.contentRows,
			scrollable:  view.scrollable,
			top:         view.top,
			maxTop:      view.maxTop,
			lineCount:   view.lineCount,
		})
	}
	for i := 0; i < view.contentRows; i++ {
		idx := view.top + i
		if idx >= len(view.lines) {
			break
		}
		drawSettingsLine(setCell, innerX, y0+1+i, width, view.lines[idx], view.cursor, opts)
	}
}

func drawSettingsLine(setCell helpSetCell, x, y, width int, line settingsLine, cursor int, opts *settingsState) {
	if line.blank {
		return
	}
	if line.header != "" {
		used := putHelpText(setCell, x, y, width, "▸ "+line.header+" ", colHelpHeader, colHelpPanelBG, protocol.StyleBold)
		for i := used + 1; i < width; i++ {
			setCell(x+i, y, '─', colHelpMuted, colHelpPanelBG, 0)
		}
		return
	}
	if !line.option {
		return
	}

	selected := line.cursor == cursor
	fg, bg := int32(colHelpFG), int32(colHelpPanelBG)
	if selected {
		fg, bg = colSettingsSelectedFG, colSettingsSelectedBG
	}
	for i := 0; i < width; i++ {
		setCell(x+i, y, ' ', fg, bg, 0)
	}

	value := "[" + opts.text(line.def) + "]"
	valueWidth := uniseg.StringWidth(value)
	if valueWidth+4 > width {
		valueWidth = max(1, width-2)
		value = fitHelpText(value, valueWidth)
	}
	labelWidth := max(1, width-valueWidth-4)
	prefix := "  "
	if selected {
		prefix = "▸ "
	}
	var style uint8
	if selected {
		style = protocol.StyleBold
	}
	putHelpText(setCell, x+1, y, labelWidth, fitHelpText(prefix+line.def.Label, labelWidth), fg, bg, style)

	valueX := x + width - valueWidth - 1
	// Color options paint the value chip with the palette they select, so the
	// row previews the bar or accent before it is applied. Other options
	// highlight their chip while selected.
	vfg, vbg := int32(colHelpKeyFG), int32(colHelpKeyBG)
	if pfg, pbg, ok := settingPreviewStyle(line.def, opts.index(line.def)); ok {
		vfg, vbg = pfg, pbg
	} else if selected {
		vfg, vbg = int32(colBarActiveFG), opts.borderColor()
	}
	for i := 0; i < valueWidth; i++ {
		setCell(valueX+i, y, ' ', vfg, vbg, protocol.StyleBold)
	}
	putHelpText(setCell, valueX, y, valueWidth, value, vfg, vbg, protocol.StyleBold)
}

// copyKeySequence maps the configured copy-selection key to the byte
// sequence the client emits for it (see encodeKey in internal/client).
// Unknown or nil settings fall back to the default, Ctrl-Y.
func copyKeySequence(st *settingsState) string {
	def, ok := settingByID(settingCopyKey)
	if !ok || st == nil {
		return "\x19"
	}
	switch st.text(def) {
	case "Ctrl-Y":
		return "\x19"
	case "Ctrl-Shift-X":
		return "\x1b[27;6;120~"
	case "Ctrl-Shift-C":
		return "\x1b[27;6;99~"
	case "Ctrl-Insert":
		return "\x1b[2;5~"
	default: // unknown value: keep the safest key
		return "\x19"
	}
}
