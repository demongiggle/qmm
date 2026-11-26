package server

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"

	"github.com/demongiggle/qmm/internal/protocol"
)

// helpItem is one shortcut in the prefix-key cheatsheet. Keys are shown as
// the complete sequence where useful; mouse shortcuts intentionally omit the
// prefix. Descriptions are deliberately short so the menu stays readable on
// small terminals.
type helpItem struct {
	Key  string
	Desc string
}

type helpSection struct {
	Title string
	Items []helpItem
}

var helpSections = []helpSection{
	{
		Title: "Panes",
		Items: []helpItem{
			{"C-a |", "Split left/right"},
			{"C-a -", "Split top/bottom"},
			{"C-a o", "Next pane"},
			{"C-a q", "Show pane numbers / jump"},
			{"C-a x", "Close pane"},
		},
	},
	{
		Title: "Windows",
		Items: []helpItem{
			{"C-a c", "New window"},
			{"C-a n / p", "Next / previous"},
		},
	},
	{
		Title: "Layout",
		Items: []helpItem{
			{"C-a z", "Floating zoom"},
			{"C-a C-←/→/↑/↓", "Resize pane (repeat)"},
		},
	},
	{
		Title: "Session",
		Items: []helpItem{
			{"C-a &", "Kill session (y/n)"},
			{"C-a d", "Detach"},
			{"C-a C-a", "Send literal ^A"},
			{"C-a s", "Settings"},
			{"C-a ?", "Show this help"},
		},
	},
	{
		Title: "Copy mode",
		Items: []helpItem{
			{"C-a [", "Scrollback / copy"},
			{"C-a ]", "Paste copied text"},
			{"Wheel up", "Scroll / enter copy"},
		},
	},
	{
		Title: "Mouse (no prefix)",
		Items: []helpItem{
			{"Click", "Focus pane"},
			{"Double-click", "Zoom / restore"},
			{"Tab click", "Switch window"},
			{"Drag", "Divider resize / text select"},
		},
	},
}

type helpLine struct {
	header string
	key    string
	desc   string
	blank  bool
}

type helpView struct {
	columns     [][]helpLine
	contentRows int
	scrollable  bool
	top         int
	maxTop      int
	lineCount   int
}

type helpSetCell func(x, y int, ch rune, fg, bg int32, style uint8)

// openHelp, closeHelp and toggleHelp keep help local to one attached client.
// Each client owns a compositor so one user's overlay never covers another
// user's screen.
func (s *Server) openHelp(c *clientConn) {
	if c.helpOpen {
		return
	}
	c.helpOpen = true
	c.helpTop = 0
	c.settingsOpen = false
	c.forceFull = true
	c.clearRepeat()
	s.broadcast()
}

func (s *Server) closeHelp(c *clientConn) {
	if !c.helpOpen {
		return
	}
	c.helpOpen = false
	c.helpTop = 0
	c.forceFull = true
	c.clearRepeat()
	s.broadcast()
}

func (s *Server) toggleHelp(c *clientConn) {
	if c.helpOpen {
		s.closeHelp(c)
		return
	}
	s.openHelp(c)
}

// handleHelpInput consumes keys while the overlay is open. Navigation keys
// scroll a compact, single-column layout; every other key closes the menu and
// is swallowed so it cannot reach the pane underneath.
func (s *Server) handleHelpInput(c *clientConn, data []byte) {
	if !c.helpOpen {
		s.handleInput(c, data)
		return
	}
	page, maxTop := helpScrollMetrics(s.cols, s.rows)
	if maxTop == 0 {
		s.closeHelp(c)
		return
	}
	seq := string(data)
	switch {
	case isHelpNav(seq, "A", "k"):
		s.helpScroll(c, -1, maxTop)
	case isHelpNav(seq, "B", "j"):
		s.helpScroll(c, 1, maxTop)
	case isHelpNav(seq, "5~", "b"), seq == "\x02":
		s.helpScroll(c, -page, maxTop)
	case isHelpNav(seq, "6~", "f"), seq == "\x06", seq == " ":
		s.helpScroll(c, page, maxTop)
	case isHelpNav(seq, "H", "g"):
		s.helpSetTop(c, 0, maxTop)
	case isHelpNav(seq, "F", "G"):
		s.helpSetTop(c, maxTop, maxTop)
	default:
		s.closeHelp(c)
	}
}

// isHelpNav matches plain and CSI-modified navigation keys. The final byte
// is enough because these sequences are only consumed in help mode.
func isHelpNav(seq, final, plain string) bool {
	if seq == plain {
		return true
	}
	if strings.HasPrefix(seq, "\x1b[") {
		return strings.HasSuffix(seq, final)
	}
	return seq == "\x1bO"+final
}

func (s *Server) helpScroll(c *clientConn, delta, maxTop int) {
	s.helpSetTop(c, c.helpTop+delta, maxTop)
}

func (s *Server) helpSetTop(c *clientConn, top, maxTop int) {
	top = max(0, min(top, maxTop))
	if top == c.helpTop {
		return
	}
	c.helpTop = top
	c.forceFull = true
	s.broadcast()
}

func helpScrollMetrics(cols, rows int) (page, maxTop int) {
	v := buildHelpView(cols, rows, 0)
	return max(1, v.contentRows), v.maxTop
}

func helpBox(cols, rows int) (x0, y0, x1, y1 int) {
	w := min(cols-2, 78)
	if w < 10 {
		w = cols
	}
	h := min(rows-3, 22)
	if h < 3 {
		h = max(rows-1, 1)
	}
	x0 = max(0, (cols-w)/2)
	y0 = max(0, (rows-1-h)/2)
	x1 = min(cols-1, x0+w-1)
	y1 = min(rows-2, y0+h-1)
	if y1 < y0 {
		y1 = y0
	}
	return x0, y0, x1, y1
}

func helpContentRows(cols, rows int) int {
	_, y0, _, y1 := helpBox(cols, rows)
	return max(1, y1-y0-1)
}

func helpInnerWidth(cols, rows int) int {
	x0, _, x1, _ := helpBox(cols, rows)
	return max(1, x1-x0-3)
}

func helpBlocks() [][]helpLine {
	blocks := make([][]helpLine, 0, len(helpSections))
	for _, sec := range helpSections {
		lines := []helpLine{{header: strings.ToUpper(sec.Title)}}
		for _, item := range sec.Items {
			lines = append(lines, helpLine{key: item.Key, desc: item.Desc})
		}
		lines = append(lines, helpLine{blank: true})
		blocks = append(blocks, lines)
	}
	return blocks
}

func trimHelpBlock(lines []helpLine) []helpLine {
	for len(lines) > 0 && lines[len(lines)-1].blank {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func packHelpBlocks(blocks [][]helpLine, count, maxRows int) ([][]helpLine, bool) {
	columns := make([][]helpLine, count)
	sizes := make([]int, count)
	for _, block := range blocks {
		best := 0
		for i := 1; i < count; i++ {
			if sizes[i] < sizes[best] {
				best = i
			}
		}
		if sizes[best]+len(block) > maxRows {
			return nil, false
		}
		columns[best] = append(columns[best], block...)
		sizes[best] += len(block)
	}
	for i := range columns {
		columns[i] = trimHelpBlock(columns[i])
	}
	return columns, true
}

func flattenHelpBlocks(blocks [][]helpLine) []helpLine {
	var lines []helpLine
	for i, block := range blocks {
		if i > 0 {
			lines = append(lines, helpLine{blank: true})
		}
		lines = append(lines, trimHelpBlock(block)...)
	}
	return lines
}

func buildHelpView(cols, rows, top int) helpView {
	blocks := helpBlocks()
	total := 0
	for _, block := range blocks {
		total += len(block)
	}
	contentRows := helpContentRows(cols, rows)
	innerWidth := helpInnerWidth(cols, rows)

	// Two readable columns on normal terminals; one scrollable column on
	// narrow or short terminals. Sections are never split across columns.
	maxColumns := min(2, max(1, (innerWidth+2)/30))
	for n := maxColumns; n >= 2; n-- {
		if total > n*contentRows {
			continue
		}
		if columns, ok := packHelpBlocks(blocks, n, contentRows); ok {
			return helpView{
				columns:     columns,
				contentRows: contentRows,
				lineCount:   total,
			}
		}
	}

	lines := flattenHelpBlocks(blocks)
	maxTop := max(0, len(lines)-contentRows)
	top = max(0, min(top, maxTop))
	end := min(len(lines), top+contentRows)
	visible := append([]helpLine(nil), lines[top:end]...)
	return helpView{
		columns:     [][]helpLine{visible},
		contentRows: contentRows,
		scrollable:  maxTop > 0,
		top:         top,
		maxTop:      maxTop,
		lineCount:   len(lines),
	}
}

// drawHelp renders a modal, color-coded cheatsheet over the composed frame.
// The backdrop and panel are opaque, so underlying pane output cannot bleed
// through the highlighted shortcuts.
func (c *compositor) drawHelp(frame *protocol.Frame, setCell helpSetCell, cols, rows int) {
	view := buildHelpView(cols, rows, c.helpTop)
	c.helpTop = view.top
	frame.CursorVisible = false
	frame.CursorX, frame.CursorY = 0, 0
	frame.Panes = nil
	frame.Splitters = nil

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
	putHelpText(setCell, x0+2, y0, titleWidth, " QMM HELP · PREFIX: Ctrl-a ", colHelpTitle, colHelpPanelBG, protocol.StyleBold)
	putHelpText(setCell, x0+2, y1, titleWidth, helpFooter(view), colHelpMuted, colHelpPanelBG, 0)

	innerX := x0 + 2
	innerRight := x1 - 2
	width := max(1, innerRight-innerX+1)
	scrollX := -1
	if view.scrollable {
		width = max(1, width-1)
		scrollX = innerRight
		drawHelpScrollbar(setCell, scrollX, y0+1, view.contentRows, view)
	}

	columns := view.columns
	if len(columns) == 0 {
		return
	}
	gap := 2
	if len(columns) > 1 && width < 2*10+gap {
		columns = columns[:1]
	}
	colWidth := (width - gap*(len(columns)-1)) / len(columns)
	if colWidth < 10 {
		colWidth = width
	}
	badgeWidth := maxHelpKeyWidth() + 2
	if badgeWidth+10 > colWidth {
		badgeWidth = max(4, colWidth-10)
	}
	for i, lines := range columns {
		x := innerX + i*(colWidth+gap)
		for j, line := range lines {
			if j >= view.contentRows {
				break
			}
			drawHelpLine(setCell, x, y0+1+j, colWidth, line, badgeWidth)
		}
	}
}

func drawHelpFrame(setCell helpSetCell, x0, y0, x1, y1 int) {
	style := protocol.StyleBold
	for x := x0; x <= x1; x++ {
		setCell(x, y0, '─', colHelpBorder, colHelpPanelBG, style)
		setCell(x, y1, '─', colHelpBorder, colHelpPanelBG, style)
	}
	for y := y0; y <= y1; y++ {
		setCell(x0, y, '│', colHelpBorder, colHelpPanelBG, style)
		setCell(x1, y, '│', colHelpBorder, colHelpPanelBG, style)
	}
	setCell(x0, y0, '╭', colHelpBorder, colHelpPanelBG, style)
	setCell(x1, y0, '╮', colHelpBorder, colHelpPanelBG, style)
	setCell(x0, y1, '╰', colHelpBorder, colHelpPanelBG, style)
	setCell(x1, y1, '╯', colHelpBorder, colHelpPanelBG, style)
}

func helpFooter(view helpView) string {
	if !view.scrollable {
		return " Esc/q close · any key closes "
	}
	first := view.top + 1
	last := min(view.lineCount, view.top+view.contentRows)
	return fmt.Sprintf(" q/Esc close · ↑/↓ · %d-%d/%d ", first, last, view.lineCount)
}

func drawHelpScrollbar(setCell helpSetCell, x, y, height int, view helpView) {
	if height < 1 {
		return
	}
	for i := 0; i < height; i++ {
		setCell(x, y+i, '│', colHelpMuted, colHelpPanelBG, 0)
	}
	thumbHeight := max(1, height*height/max(1, view.lineCount))
	if thumbHeight > height {
		thumbHeight = height
	}
	thumbY := y
	if view.maxTop > 0 && height > thumbHeight {
		thumbY += (height - thumbHeight) * view.top / view.maxTop
	}
	for i := 0; i < thumbHeight; i++ {
		setCell(x, thumbY+i, '█', colHelpTitle, colHelpPanelBG, protocol.StyleBold)
	}
}

func drawHelpLine(setCell helpSetCell, x, y, width int, line helpLine, badgeWidth int) {
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
	badgeWidth = min(badgeWidth, max(4, width-4))
	for i := 0; i < badgeWidth; i++ {
		setCell(x+i, y, ' ', colHelpKeyFG, colHelpKeyBG, protocol.StyleBold)
	}
	putHelpText(setCell, x+1, y, max(1, badgeWidth-2), fitHelpText(line.key, max(1, badgeWidth-2)), colHelpKeyFG, colHelpKeyBG, protocol.StyleBold)
	descWidth := width - badgeWidth - 2
	if descWidth > 0 {
		putHelpText(setCell, x+badgeWidth+2, y, descWidth, fitHelpText(line.desc, descWidth), colHelpFG, colHelpPanelBG, 0)
	}
}

func maxHelpKeyWidth() int {
	width := 0
	for _, sec := range helpSections {
		for _, item := range sec.Items {
			width = max(width, uniseg.StringWidth(item.Key))
		}
	}
	return width
}

func fitHelpText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if uniseg.StringWidth(text) <= width {
		return text
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		rw := uniseg.StringWidth(string(r))
		if used+rw > width-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

func putHelpText(setCell helpSetCell, x, y, width int, text string, fg, bg int32, style uint8) int {
	pos := 0
	for _, r := range text {
		rw := uniseg.StringWidth(string(r))
		if rw <= 0 {
			rw = 1
		}
		if pos+rw > width {
			break
		}
		setCell(x+pos, y, r, fg, bg, style)
		if rw == 2 && pos+1 < width {
			setCell(x+pos+1, y, ' ', fg, bg, style)
		}
		pos += rw
	}
	return pos
}
