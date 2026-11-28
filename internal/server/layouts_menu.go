package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

// openLayoutsMenu opens the per-client saved-layout menu (prefix+L). It reloads
// the layouts file first so external edits and saves from other servers appear.
func (s *Server) openLayoutsMenu(c *clientConn) {
	if c.menu == menuLayouts {
		return
	}
	c.confirmOpen = false
	c.displayPanesOpen = false
	c.displayPanesInput = ""
	c.displayPanesDeadline = time.Time{}
	c.windowGotoOpen = false
	c.windowGotoInput = ""
	c.windowGotoDeadline = time.Time{}
	c.copyMode = nil
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()

	warning := s.reloadLayouts()
	c.setMenu(menuLayouts)
	c.layoutsList = append([]savedLayout(nil), s.layouts...)
	if warning != nil {
		c.layoutsError = layoutWarningMessage(*warning)
	} else if s.layoutsErr != "" {
		c.layoutsError = s.layoutsErr
	}
	c.forceFull = true
	s.broadcast()
}

func (s *Server) closeLayoutsMenu(c *clientConn) {
	if c.menu != menuLayouts {
		return
	}
	c.setMenu(menuNone)
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.forceFull = true
	s.broadcast()
}

func (s *Server) layoutsMenuMoveCursor(c *clientConn, delta int) {
	total := len(c.layoutsList) + 1 // saved layouts + save row
	if total <= 0 {
		return
	}
	c.layoutsCursor = max(0, min(c.layoutsCursor+delta, total-1))
	c.layoutsError = ""
	c.layoutsMessage = ""
	c.layoutsConfirmOverwrite = ""
	c.layoutsConfirmDelete = ""
	c.forceFull = true
	s.broadcast()
}

func (s *Server) startLayoutsNaming(c *clientConn) {
	c.layoutsNaming = true
	c.layoutsConfirmOverwrite = ""
	c.layoutsConfirmDelete = ""
	c.layoutsCursor = len(c.layoutsList)
	c.layoutsInput = ""
	c.layoutsError = ""
	c.layoutsMessage = ""
	c.forceFull = true
	s.broadcast()
}

func (s *Server) handleLayoutsMenuInput(c *clientConn, data []byte) {
	if c.layoutsConfirmOverwrite != "" || c.layoutsConfirmDelete != "" {
		s.handleLayoutsConfirmInput(c, data)
		return
	}
	if c.layoutsNaming {
		s.handleLayoutsNamingInput(c, data)
		return
	}

	seq := string(data)
	page := max(1, helpContentRows(s.cols, s.rows))
	total := len(c.layoutsList) + 1
	switch {
	case isHelpNav(seq, "A", "k"):
		s.layoutsMenuMoveCursor(c, -1)
	case isHelpNav(seq, "B", "j"):
		s.layoutsMenuMoveCursor(c, 1)
	case isHelpNav(seq, "5~", "b"), seq == "\x02":
		s.layoutsMenuMoveCursor(c, -page)
	case isHelpNav(seq, "6~", "f"), seq == "\x06":
		s.layoutsMenuMoveCursor(c, page)
	case isHelpNav(seq, "H", "g"):
		s.layoutsMenuMoveCursor(c, -total)
	case isHelpNav(seq, "F", "G"):
		s.layoutsMenuMoveCursor(c, total)
	case seq == "\r", seq == "\n":
		if c.layoutsCursor == len(c.layoutsList) {
			s.startLayoutsNaming(c)
			return
		}
		s.restoreSelectedLayout(c)
	case seq == "s", seq == "S":
		s.startLayoutsNaming(c)
	case seq == "d", seq == "D":
		s.startLayoutsDelete(c)
	case seq == "\x1b", seq == "\x03", seq == "q", seq == "Q", seq == "L", seq == "\x01L":
		s.closeLayoutsMenu(c)
	default:
		// Keep the menu open; stray keys must not reach the pane underneath.
	}
}

func (s *Server) handleLayoutsConfirmInput(c *clientConn, data []byte) {
	seq := string(data)
	switch seq {
	case "y", "Y":
		if name := c.layoutsConfirmDelete; name != "" {
			s.deleteLayoutNamed(c, name)
			return
		}
		if name := c.layoutsConfirmOverwrite; name != "" {
			s.saveLayoutNamed(c, name)
		}
	case "n", "N", "\x1b", "\x03":
		if name := c.layoutsConfirmOverwrite; name != "" {
			// Return to the name prompt with the existing name pre-filled so
			// it can be edited instead of retyped.
			c.layoutsConfirmOverwrite = ""
			c.layoutsNaming = true
			c.layoutsInput = name
			c.layoutsMessage = ""
			c.layoutsError = ""
			c.forceFull = true
			s.broadcast()
			return
		}
		c.layoutsConfirmDelete = ""
		c.layoutsMessage = ""
		c.layoutsError = ""
		c.forceFull = true
		s.broadcast()
	}
}

func (s *Server) handleLayoutsNamingInput(c *clientConn, data []byte) {
	changed := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch ch {
		case 0x1b, 0x03:
			c.layoutsNaming = false
			c.layoutsInput = ""
			c.layoutsError = ""
			c.forceFull = true
			s.broadcast()
			return
		case '\r', '\n':
			name := strings.TrimSpace(c.layoutsInput)
			if name == "" {
				name = nextLayoutName(c.layoutsList)
			}
			if err := validateLayoutName(name); err != nil {
				c.layoutsError = err.Error()
				c.forceFull = true
				s.broadcast()
				return
			}
			if layoutIndex(c.layoutsList, name) >= 0 {
				c.layoutsConfirmOverwrite = name
				c.layoutsNaming = false
				c.layoutsMessage = fmt.Sprintf("Layout %q exists — overwrite? y/n", name)
				c.layoutsError = ""
				c.forceFull = true
				s.broadcast()
				return
			}
			s.saveLayoutNamed(c, name)
			return
		case 0x7f, 0x08:
			if len(c.layoutsInput) > 0 {
				c.layoutsInput = c.layoutsInput[:len(c.layoutsInput)-1]
				c.layoutsError = ""
				changed = true
			}
		default:
			ok := (ch >= 'a' && ch <= 'z') ||
				(ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') ||
				ch == '.' || ch == '_' || ch == '-'
			if !ok || len(c.layoutsInput) >= maxLayoutNameLen {
				continue
			}
			c.layoutsInput += string(ch)
			c.layoutsError = ""
			changed = true
		}
	}
	if changed {
		c.forceFull = true
		s.broadcast()
	}
}

func (s *Server) saveLayoutNamed(c *clientConn, name string) {
	_, existed, err := s.saveCurrentLayout(name)
	if err != nil {
		c.layoutsError = err.Error()
		c.layoutsNaming = true
		c.layoutsInput = name
		c.layoutsConfirmOverwrite = ""
		c.layoutsMessage = ""
		c.forceFull = true
		s.broadcast()
		return
	}
	c.layoutsNaming = false
	c.layoutsInput = ""
	c.layoutsConfirmOverwrite = ""
	c.layoutsError = ""
	verb := "Saved"
	if existed {
		verb = "Updated"
	}
	c.layoutsMessage = fmt.Sprintf("%s layout %q", verb, name)
	s.syncLayoutsMenuClients(name)
	c.forceFull = true
	s.broadcast()
}

func (s *Server) startLayoutsDelete(c *clientConn) {
	if c.layoutsCursor < 0 || c.layoutsCursor >= len(c.layoutsList) {
		c.layoutsMessage = "Select a saved layout to delete"
		c.layoutsError = ""
		c.forceFull = true
		s.broadcast()
		return
	}
	name := c.layoutsList[c.layoutsCursor].Name
	c.layoutsConfirmDelete = name
	c.layoutsMessage = fmt.Sprintf("Delete layout %q? y/n", name)
	c.layoutsError = ""
	c.forceFull = true
	s.broadcast()
}

func (s *Server) deleteLayoutNamed(c *clientConn, name string) {
	if err := s.deleteSavedLayout(name); err != nil {
		c.layoutsError = err.Error()
		c.layoutsConfirmDelete = ""
		c.layoutsMessage = ""
		c.forceFull = true
		s.broadcast()
		return
	}
	c.layoutsConfirmDelete = ""
	c.layoutsError = ""
	c.layoutsMessage = fmt.Sprintf("Deleted layout %q", name)
	s.syncLayoutsMenuClients("")
	c.forceFull = true
	s.broadcast()
}

// syncLayoutsMenuClients refreshes every open layout menu after a save or
// delete. preferred selects a row by name when it exists.
func (s *Server) syncLayoutsMenuClients(preferred string) {
	for c := range s.clients {
		if c.menu != menuLayouts {
			continue
		}
		prev := ""
		if c.layoutsCursor >= 0 && c.layoutsCursor < len(c.layoutsList) {
			prev = c.layoutsList[c.layoutsCursor].Name
		}
		c.layoutsList = append([]savedLayout(nil), s.layouts...)
		target := preferred
		if target == "" {
			target = prev
		}
		c.layoutsCursor = 0
		if target != "" {
			if idx := layoutIndex(c.layoutsList, target); idx >= 0 {
				c.layoutsCursor = idx
			}
		}
		if c.layoutsCursor > len(c.layoutsList) {
			c.layoutsCursor = len(c.layoutsList)
		}
		c.forceFull = true
	}
}

func (s *Server) restoreSelectedLayout(c *clientConn) {
	if c.layoutsCursor < 0 || c.layoutsCursor >= len(c.layoutsList) {
		return
	}
	name := c.layoutsList[c.layoutsCursor].Name
	idx := layoutIndex(s.layouts, name)
	if idx < 0 {
		s.reloadLayouts()
		idx = layoutIndex(s.layouts, name)
	}
	if idx < 0 {
		c.layoutsError = fmt.Sprintf("layout %q not found", name)
		c.forceFull = true
		s.broadcast()
		return
	}
	if err := s.restoreLayout(c, s.layouts[idx]); err != nil {
		c.layoutsError = err.Error()
		c.forceFull = true
		s.broadcast()
		return
	}
	s.closeLayoutsMenu(c)
}

// --- menu view -------------------------------------------------------------

type layoutsMenuLine struct {
	header   string
	layout   *savedLayout
	isSave   bool
	isHint   bool
	hintText string
	isError  bool
	errText  string
	blank    bool
	itemIdx  int // -1 for non-selectable rows
}

type layoutsMenuView struct {
	lines       []layoutsMenuLine
	contentRows int
	top         int
	maxTop      int
	scrollable  bool
	lineCount   int
	cursor      int
}

func buildLayoutsMenuView(cols, rows, cursor, top int, list []savedLayout, naming bool, input, errText, message string) layoutsMenuView {
	lines := make([]layoutsMenuLine, 0, len(list)+8)
	lines = append(lines, layoutsMenuLine{header: "Saved Layouts", itemIdx: -1})
	for i := range list {
		l := list[i]
		lines = append(lines, layoutsMenuLine{layout: &l, itemIdx: i})
	}
	if len(list) == 0 {
		lines = append(lines, layoutsMenuLine{isHint: true, hintText: "No saved layouts yet.", itemIdx: -1})
	}
	lines = append(lines, layoutsMenuLine{blank: true, itemIdx: -1})
	lines = append(lines, layoutsMenuLine{isSave: true, itemIdx: len(list)})
	if errText != "" {
		lines = append(lines, layoutsMenuLine{isError: true, errText: errText, itemIdx: -1})
	}
	if message != "" {
		lines = append(lines, layoutsMenuLine{isHint: true, hintText: message, itemIdx: -1})
	}

	totalItems := len(list) + 1
	cursor = max(0, min(cursor, totalItems-1))
	selectedLine := 0
	for i, line := range lines {
		if line.itemIdx == cursor {
			selectedLine = i
			break
		}
	}
	// Keep the prompt/status tail visible with the selected row.
	tail := 0
	if errText != "" {
		tail++
	}
	if message != "" {
		tail++
	}
	selectedLine = min(selectedLine+tail, len(lines)-1)

	contentRows := helpContentRows(cols, rows)
	maxTop := max(0, len(lines)-contentRows)
	if selectedLine < top {
		top = selectedLine
	}
	if selectedLine >= top+contentRows {
		top = selectedLine - contentRows + 1
	}
	top = max(0, min(top, maxTop))

	return layoutsMenuView{
		lines:       lines,
		contentRows: contentRows,
		top:         top,
		maxTop:      maxTop,
		scrollable:  maxTop > 0,
		lineCount:   len(lines),
		cursor:      cursor,
	}
}

func layoutsMenuFooter(view layoutsMenuView, naming bool, confirmOverwrite, confirmDelete bool) string {
	switch {
	case confirmOverwrite:
		return " y overwrite · n/Esc cancel "
	case confirmDelete:
		return " y delete · n/Esc cancel "
	case naming:
		return " Type name · Enter save · Esc cancel "
	}
	if view.scrollable {
		first := view.top + 1
		last := min(view.lineCount, view.top+view.contentRows)
		return fmt.Sprintf(" ↑/↓ Enter restore · s save · d delete · %d-%d/%d · q close ", first, last, view.lineCount)
	}
	return " ↑/↓ Enter restore · s save · d delete · q/Esc close "
}

func (c *compositor) drawLayoutsMenu(frame *protocol.Frame, setCell helpSetCell, cols, rows int, nextAuto string) {
	if cols < minCols || rows < minRows {
		return
	}
	view := buildLayoutsMenuView(cols, rows, c.layoutsCursor, c.layoutsTop, c.layoutsList, c.layoutsNaming, c.layoutsInput, c.layoutsError, c.layoutsMessage)
	c.layoutsCursor, c.layoutsTop = view.cursor, view.top

	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0
	frame.Panes = nil
	frame.Splitters = nil
	frame.Tabs = nil

	for y := 0; y < max(0, rows-statusRows); y++ {
		for x := 0; x < cols; x++ {
			setCell(x, y, ' ', c.chrome.help.muted, c.chrome.help.backdrop, 0)
		}
	}

	x0, y0, x1, y1 := helpBox(cols, rows)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			setCell(x, y, ' ', c.chrome.help.fg, c.chrome.help.panelBG, 0)
		}
	}
	drawHelpFrame(setCell, x0, y0, x1, y1, c.chrome.help)

	titleWidth := max(0, x1-x0-3)
	putHelpText(setCell, x0+2, y0, titleWidth, " QMM LAYOUTS · PREFIX: Ctrl-a ", c.chrome.help.title, c.chrome.help.panelBG, protocol.StyleBold)
	footer := layoutsMenuFooter(view, c.layoutsNaming, c.layoutsConfirmOverwrite != "", c.layoutsConfirmDelete != "")
	putHelpText(setCell, x0+2, y1, titleWidth, fitHelpText(footer, titleWidth), c.chrome.help.muted, c.chrome.help.panelBG, 0)

	innerX := x0 + 2
	innerRight := x1 - 2
	width := max(1, innerRight-innerX+1)
	if view.scrollable {
		width = max(1, width-1)
		drawHelpScrollbar(setCell, innerRight, y0+1, view.contentRows, helpView{
			contentRows: view.contentRows,
			scrollable:  view.scrollable,
			top:         view.top,
			maxTop:      view.maxTop,
			lineCount:   view.lineCount,
		}, c.chrome.help)
	}
	for i := 0; i < view.contentRows; i++ {
		idx := view.top + i
		if idx >= len(view.lines) {
			break
		}
		c.drawLayoutsMenuLine(setCell, innerX, y0+1+i, width, view.lines[idx], view.cursor, c.layoutsNaming, c.layoutsInput, nextAuto)
	}
}

func (c *compositor) drawLayoutsMenuLine(setCell helpSetCell, x, y, width int, line layoutsMenuLine, cursor int, naming bool, input, nextAuto string) {
	if line.blank {
		return
	}
	if line.header != "" {
		used := putHelpText(setCell, x, y, width, "▸ "+line.header+" ", c.chrome.help.header, c.chrome.help.panelBG, protocol.StyleBold)
		for i := used + 1; i < width; i++ {
			setCell(x+i, y, '─', c.chrome.help.muted, c.chrome.help.panelBG, 0)
		}
		return
	}
	if line.isError {
		putHelpText(setCell, x+2, y, max(1, width-2), fitHelpText("Error: "+line.errText, max(1, width-2)), c.chrome.tooSmall, c.chrome.help.panelBG, protocol.StyleBold)
		return
	}
	if line.isHint {
		putHelpText(setCell, x+2, y, max(1, width-2), fitHelpText(line.hintText, max(1, width-2)), c.chrome.help.title, c.chrome.help.panelBG, 0)
		return
	}

	selected := line.itemIdx == cursor
	fg, bg := c.chrome.help.fg, c.chrome.help.panelBG
	if selected {
		fg, bg = c.chrome.settingsFG, c.chrome.settingsBG
	}
	for i := 0; i < width; i++ {
		setCell(x+i, y, ' ', fg, bg, 0)
	}

	prefix := "  "
	var style uint8
	if selected {
		prefix = "▸ "
		style = protocol.StyleBold
	}

	if line.isSave {
		if naming {
			prompt := fmt.Sprintf("Save layout as: %s█", input)
			if input == "" {
				prompt = fmt.Sprintf("Save layout as: █ (Enter: auto %q)", nextAuto)
			}
			putHelpText(setCell, x+1, y, max(1, width-2), fitHelpText(prefix+prompt, max(1, width-2)), c.chrome.help.title, bg, protocol.StyleBold)
			return
		}
		putHelpText(setCell, x+1, y, max(1, width-2), fitHelpText(prefix+"+ Save current window", max(1, width-2)), c.chrome.help.title, bg, style)
		return
	}

	if line.layout == nil {
		return
	}
	l := *line.layout
	meta := fmt.Sprintf("%d pane", len(l.Panes))
	if len(l.Panes) != 1 {
		meta += "s"
	}
	rightWidth := uniseg.StringWidth(meta)
	if rightWidth+6 > width {
		rightWidth = 0
	}
	nameWidth := max(1, width-rightWidth-3)
	putHelpText(setCell, x+1, y, nameWidth, fitHelpText(prefix+l.Name, nameWidth), fg, bg, style)
	if rightWidth > 0 {
		putHelpText(setCell, x+width-rightWidth-1, y, rightWidth, meta, c.chrome.help.muted, bg, 0)
	}
}
