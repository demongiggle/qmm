package server

import (
	"fmt"
	"os"
	"time"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/sessions"
)

// sessionListResult carries an asynchronous session-directory scan back into
// the server's single event loop so socket probing never blocks PTY I/O.
type sessionListResult struct {
	list []protocol.SessionInfo
}

func (s *Server) sessionName() string {
	if s != nil && s.name != "" {
		return s.name
	}
	if s != nil && s.sess != nil && s.sess.name != "" {
		return s.sess.name
	}
	return sessions.DefaultName
}

func (s *Server) sessionInfo() protocol.SessionInfo {
	var wins, panes int
	if s.sess != nil {
		wins = len(s.sess.windows)
		panes = len(s.sess.panes)
	}
	created := s.createdAt.Unix()
	if s.createdAt.IsZero() {
		created = time.Now().Unix()
	}
	return protocol.SessionInfo{
		Name:      s.sessionName(),
		Windows:   wins,
		Panes:     panes,
		Clients:   len(s.clients),
		CreatedAt: created,
		Version:   s.version,
		PID:       os.Getpid(),
	}
}

// openSessionsMenu opens the per-client session switcher/creator overlay
// (prefix+S). It renders immediately with the current session and kicks off
// an asynchronous scan of the socket directory to populate all live sessions.
func (s *Server) openSessionsMenu(c *clientConn) {
	c.helpOpen = false
	c.helpTop = 0
	c.confirmOpen = false
	c.settingsOpen = false
	c.displayPanesOpen = false
	c.displayPanesInput = ""
	c.displayPanesDeadline = time.Time{}
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()

	c.sessionsOpen = true
	c.sessionsLoading = true
	c.sessionsList = []protocol.SessionInfo{s.sessionInfo()}
	c.sessionsCursor = 0
	c.sessionsTop = 0
	c.sessionsNaming = false
	c.sessionsInput = ""
	c.sessionsError = ""
	c.forceFull = true
	s.broadcast()

	s.refreshSessionsMenuAsync(c)
}

func (s *Server) refreshSessionsMenuAsync(c *clientConn) {
	if s.msgs == nil {
		return
	}
	dir := s.cfg.SocketDir
	go func() {
		list, _ := sessions.List(dir)
		panicSafe(func() {
			s.msgs <- clientMsg{cli: c, msg: sessionListResult{list: list}}
		})
	}()
}

func (s *Server) handleSessionListResult(c *clientConn, res sessionListResult) {
	if c == nil || !c.sessionsOpen {
		return
	}
	// Remember what the cursor pointed to before refreshing so the highlight
	// stays on the same session when new entries appear.
	prevOnNew := c.sessionsCursor == len(c.sessionsList)
	prevName := ""
	if !prevOnNew && c.sessionsCursor >= 0 && c.sessionsCursor < len(c.sessionsList) {
		prevName = c.sessionsList[c.sessionsCursor].Name
	}

	cur := s.sessionInfo()
	list := make([]protocol.SessionInfo, 0, len(res.list)+1)
	foundSelf := false
	for _, item := range res.list {
		if item.Name == cur.Name {
			list = append(list, cur)
			foundSelf = true
		} else {
			list = append(list, item)
		}
	}
	if !foundSelf {
		list = append([]protocol.SessionInfo{cur}, list...)
	}

	c.sessionsList = list
	c.sessionsLoading = false
	if prevOnNew {
		c.sessionsCursor = len(list)
	} else {
		target := prevName
		if target == "" {
			target = cur.Name
		}
		c.sessionsCursor = 0
		for i, item := range list {
			if item.Name == target {
				c.sessionsCursor = i
				break
			}
		}
	}
	c.forceFull = true
	s.broadcast()
}

func (s *Server) closeSessionsMenu(c *clientConn) {
	if !c.sessionsOpen {
		return
	}
	c.sessionsOpen = false
	c.sessionsLoading = false
	c.sessionsNaming = false
	c.sessionsInput = ""
	c.sessionsError = ""
	c.prefixArmed = false
	c.prefixBuf = nil
	c.clearRepeat()
	c.forceFull = true
	s.broadcast()
}

func (s *Server) handleSessionsMenuInput(c *clientConn, data []byte) {
	if c.sessionsNaming {
		s.handleSessionsNamingInput(c, data)
		return
	}
	seq := string(data)
	totalItems := len(c.sessionsList) + 1 // sessions + "+ New session"
	page := max(1, helpContentRows(s.cols, s.rows))

	switch {
	case isHelpNav(seq, "A", "k"):
		s.sessionsMoveCursor(c, -1, totalItems)
	case isHelpNav(seq, "B", "j"):
		s.sessionsMoveCursor(c, 1, totalItems)
	case isHelpNav(seq, "5~", "b"), seq == "\x02":
		s.sessionsMoveCursor(c, -page, totalItems)
	case isHelpNav(seq, "6~", "f"), seq == "\x06":
		s.sessionsMoveCursor(c, page, totalItems)
	case isHelpNav(seq, "H", "g"):
		s.sessionsMoveCursor(c, -totalItems, totalItems)
	case isHelpNav(seq, "F", "G"):
		s.sessionsMoveCursor(c, totalItems, totalItems)
	case seq == "\r", seq == "\n":
		if c.sessionsCursor == len(c.sessionsList) {
			s.startSessionsNaming(c)
			return
		}
		if c.sessionsCursor >= 0 && c.sessionsCursor < len(c.sessionsList) {
			target := c.sessionsList[c.sessionsCursor]
			if target.Name == s.sessionName() {
				s.closeSessionsMenu(c)
				return
			}
			s.switchClientSession(c, target.Name, false)
		}
	case seq == "n", seq == "N":
		s.startSessionsNaming(c)
	case seq == "r", seq == "R", seq == "\x12":
		c.sessionsLoading = true
		c.forceFull = true
		s.broadcast()
		s.refreshSessionsMenuAsync(c)
	case seq == "\x1b", seq == "\x03", seq == "q", seq == "Q", seq == "S", seq == "\x01S":
		s.closeSessionsMenu(c)
	}
}

func (s *Server) sessionsMoveCursor(c *clientConn, delta, total int) {
	if total <= 0 {
		return
	}
	c.sessionsCursor = max(0, min(c.sessionsCursor+delta, total-1))
	c.forceFull = true
	s.broadcast()
}

func (s *Server) startSessionsNaming(c *clientConn) {
	c.sessionsNaming = true
	c.sessionsCursor = len(c.sessionsList)
	c.sessionsInput = ""
	c.sessionsError = ""
	c.forceFull = true
	s.broadcast()
}

func (s *Server) handleSessionsNamingInput(c *clientConn, data []byte) {
	changed := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch ch {
		case 0x1b, 0x03:
			c.sessionsNaming = false
			c.sessionsInput = ""
			c.sessionsError = ""
			c.forceFull = true
			s.broadcast()
			return
		case '\r', '\n':
			name := c.sessionsInput
			if name == "" {
				name = sessions.NextName(c.sessionsList)
			} else {
				if err := sessions.ValidateName(name); err != nil {
					c.sessionsError = err.Error()
					c.forceFull = true
					s.broadcast()
					return
				}
				for _, existing := range c.sessionsList {
					if existing.Name == name {
						c.sessionsError = fmt.Sprintf("session %q already exists", name)
						c.forceFull = true
						s.broadcast()
						return
					}
				}
			}
			s.switchClientSession(c, name, true)
			return
		case 0x7f, 0x08:
			if len(c.sessionsInput) > 0 {
				c.sessionsInput = c.sessionsInput[:len(c.sessionsInput)-1]
				c.sessionsError = ""
				changed = true
			}
		default:
			ok := (ch >= 'a' && ch <= 'z') ||
				(ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') ||
				ch == '.' || ch == '_' || ch == '-'
			if !ok || len(c.sessionsInput) >= 64 {
				continue
			}
			c.sessionsInput += string(ch)
			c.sessionsError = ""
			changed = true
		}
	}
	if changed {
		c.forceFull = true
		s.broadcast()
	}
}

type sessionsMenuLine struct {
	header  string
	info    *protocol.SessionInfo
	isNew   bool
	isErr   bool
	errText string
	blank   bool
	itemIdx int // -1 for non-selectable rows
}

type sessionsMenuView struct {
	lines       []sessionsMenuLine
	contentRows int
	top         int
	maxTop      int
	scrollable  bool
	lineCount   int
	cursor      int
}

func buildSessionsMenuView(cols, rows, cursor, top int, list []protocol.SessionInfo, naming bool, errText string) sessionsMenuView {
	lines := make([]sessionsMenuLine, 0, len(list)+5)
	lines = append(lines, sessionsMenuLine{header: "Active Sessions", itemIdx: -1})
	for i := range list {
		info := list[i]
		lines = append(lines, sessionsMenuLine{info: &info, itemIdx: i})
	}
	lines = append(lines, sessionsMenuLine{blank: true, itemIdx: -1})
	lines = append(lines, sessionsMenuLine{isNew: true, itemIdx: len(list)})
	if naming && errText != "" {
		lines = append(lines, sessionsMenuLine{isErr: true, errText: errText, itemIdx: -1})
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
	if naming && errText != "" && selectedLine+1 < len(lines) {
		selectedLine++ // keep the error line visible beneath the input
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

	return sessionsMenuView{
		lines:       lines,
		contentRows: contentRows,
		top:         top,
		maxTop:      maxTop,
		scrollable:  maxTop > 0,
		lineCount:   len(lines),
		cursor:      cursor,
	}
}

func sessionsMenuFooter(view sessionsMenuView, loading, naming bool) string {
	if naming {
		return " Type name · Enter create · Esc back "
	}
	if loading {
		return " Scanning sessions… · q/Esc close "
	}
	if view.scrollable {
		first := view.top + 1
		last := min(view.lineCount, view.top+view.contentRows)
		return fmt.Sprintf(" ↑/↓ Enter switch · n new · r refresh · %d-%d/%d · q close ", first, last, view.lineCount)
	}
	return " ↑/↓ Enter switch · n new · r refresh · q/Esc close "
}

func (c *compositor) drawSessionsMenu(frame *protocol.Frame, setCell helpSetCell, cols, rows int, currentName string, opts *settingsState) {
	if cols < minCols || rows < minRows {
		return
	}
	if opts == nil {
		opts = &settingsState{}
	}
	view := buildSessionsMenuView(cols, rows, c.sessionsCursor, c.sessionsTop, c.sessionsList, c.sessionsNaming, c.sessionsError)
	c.sessionsCursor, c.sessionsTop = view.cursor, view.top

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
	title := fmt.Sprintf(" QMM SESSIONS · CURRENT: %s ", currentName)
	putHelpText(setCell, x0+2, y0, titleWidth, fitHelpText(title, titleWidth), c.chrome.help.title, c.chrome.help.panelBG, protocol.StyleBold)
	putHelpText(setCell, x0+2, y1, titleWidth, fitHelpText(sessionsMenuFooter(view, c.sessionsLoading, c.sessionsNaming), titleWidth), c.chrome.help.muted, c.chrome.help.panelBG, 0)

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

	nextAuto := sessions.NextName(c.sessionsList)
	for i := 0; i < view.contentRows; i++ {
		idx := view.top + i
		if idx >= len(view.lines) {
			break
		}
		c.drawSessionsMenuLine(setCell, innerX, y0+1+i, width, view.lines[idx], view.cursor, currentName, c.sessionsNaming, c.sessionsInput, nextAuto, opts)
	}
}

func (c *compositor) drawSessionsMenuLine(
	setCell helpSetCell,
	x, y, width int,
	line sessionsMenuLine,
	cursor int,
	currentName string,
	naming bool,
	input string,
	nextAuto string,
	opts *settingsState,
) {
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
	if line.isErr {
		putHelpText(setCell, x+2, y, max(1, width-2), fitHelpText("Error: "+line.errText, max(1, width-2)), c.chrome.tooSmall, c.chrome.help.panelBG, protocol.StyleBold)
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

	if line.isNew {
		if naming {
			prompt := fmt.Sprintf("New session: %s█", input)
			if input == "" {
				prompt = fmt.Sprintf("New session: █ (Enter: auto %q)", nextAuto)
			}
			putHelpText(setCell, x+1, y, max(1, width-2), fitHelpText(prefix+prompt, max(1, width-2)), c.chrome.help.title, bg, protocol.StyleBold)
			return
		}
		putHelpText(setCell, x+1, y, max(1, width-2), fitHelpText(prefix+"+ New session", max(1, width-2)), c.chrome.help.title, bg, style)
		return
	}

	if line.info == nil {
		return
	}
	info := *line.info
	isCurrent := info.Name == currentName

	meta := fmt.Sprintf("%d win · %d pane", info.Windows, info.Panes)
	if info.Panes != 1 {
		meta += "s"
	}
	if info.Clients > 0 {
		meta += fmt.Sprintf(" · %d att", info.Clients)
	}
	if info.Version != "" {
		meta += " · " + info.Version
	}

	badge := ""
	if isCurrent {
		badge = " [current] "
	}

	rightWidth := uniseg.StringWidth(meta) + uniseg.StringWidth(badge)
	if rightWidth+6 > width {
		meta = ""
		rightWidth = uniseg.StringWidth(badge)
	}
	labelWidth := max(1, width-rightWidth-3)
	putHelpText(setCell, x+1, y, labelWidth, fitHelpText(prefix+info.Name, labelWidth), fg, bg, style)

	rx := x + width - 1
	if badge != "" {
		bw := uniseg.StringWidth(badge)
		rx -= bw
		bfg, bbg := c.chrome.activeFG, opts.borderColor()
		for i := 0; i < bw; i++ {
			setCell(rx+i, y, ' ', bfg, bbg, protocol.StyleBold)
		}
		putHelpText(setCell, rx, y, bw, badge, bfg, bbg, protocol.StyleBold)
		rx--
	}
	if meta != "" {
		mw := uniseg.StringWidth(meta)
		rx -= mw
		if rx > x+1 {
			putHelpText(setCell, rx, y, mw, meta, c.chrome.help.muted, bg, 0)
		}
	}
}
