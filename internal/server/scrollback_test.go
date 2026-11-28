package server

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/protocol"
)

func scrollbackTexts(r *scrollback) []string {
	lines := r.History()
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text()
	}
	return out
}

func writeScrollbackLines(r io.Writer, n int, prefix string) {
	for i := 1; i <= n; i++ {
		_, _ = r.Write([]byte(fmt.Sprintf("%s-%d\r\n", prefix, i)))
	}
}

func TestScrollbackKeepsLinesThatScrollOff(t *testing.T) {
	p := &pane{}
	p.initTerminal(20, 4)
	r := &p.sb
	writeScrollbackLines(p.vt, 5, "line")
	got := scrollbackTexts(r)
	if strings.Join(got, "|") != "line-1|line-2" {
		t.Fatalf("history = %q, want line-1|line-2", got)
	}
	// The visible rows are not part of scrollback; copy mode adds them from
	// the real vt10x screen when it opens.
	for _, text := range got {
		if strings.Contains(text, "line-5") {
			t.Fatalf("live line leaked into history: %q", got)
		}
	}
}

func TestScrollbackRingLimitAndClearHistory(t *testing.T) {
	p := &pane{}
	p.initTerminal(20, 3)
	r := &p.sb
	writeScrollbackLines(p.vt, scrollbackLimit+5, "ln")
	got := scrollbackTexts(r)
	if len(got) != scrollbackLimit {
		t.Fatalf("history length = %d, want %d", len(got), scrollbackLimit)
	}
	for i, line := range got {
		if want := fmt.Sprintf("ln-%d", i+4); line != want {
			t.Fatalf("history[%d] = %q, want %q", i, line, want)
		}
	}
	_, _ = p.vt.Write([]byte("\x1b[3J"))
	if got := scrollbackTexts(r); len(got) != 0 {
		t.Fatalf("CSI 3 J should clear history, got %q", got)
	}
}

func TestScrollbackIgnoresAlternateScreen(t *testing.T) {
	p := &pane{}
	p.initTerminal(20, 3)
	r := &p.sb
	_, _ = p.vt.Write([]byte("\x1b[?1049h"))
	writeScrollbackLines(p.vt, 6, "alt")
	if got := scrollbackTexts(r); len(got) != 0 {
		t.Fatalf("alternate screen redraws leaked into history: %q", got)
	}
	_, _ = p.vt.Write([]byte("\x1b[?1049l"))
	writeScrollbackLines(p.vt, 4, "main")
	if got := strings.Join(scrollbackTexts(r), "|"); got != "main-1|main-2" {
		t.Fatalf("history after alt screen = %q, want main-1", got)
	}
}

func TestScrollbackCSIAndSGR(t *testing.T) {
	p := &pane{}
	p.initTerminal(20, 3)
	r := &p.sb
	_, _ = p.vt.Write([]byte("\x1b[1;31mbold red\x1b[0m\r\nplain\r\n"))
	_, _ = p.vt.Write([]byte("\x1b[2S")) // scroll two more lines off the top
	lines := r.History()
	if len(lines) != 2 {
		t.Fatalf("history length = %d, want 2: %q", len(lines), scrollbackTexts(r))
	}
	if lines[0].text() != "bold red" {
		t.Fatalf("first history line = %q, want bold red", lines[0].text())
	}
	c := lines[0].cells[0]
	if c.FG != 9 || c.Style&protocol.StyleBold == 0 {
		t.Fatalf("first cell = %+v, want red foreground and bold", c)
	}
	if c = lines[1].cells[0]; c.FG != protocol.ColorDefault || c.Style != 0 {
		t.Fatalf("reset cell = %+v, want default style", c)
	}
}

func TestScrollbackRetainsWideRuneSplitAcrossWrites(t *testing.T) {
	p := &pane{}
	p.initTerminal(10, 2)
	r := &p.sb
	chunks := []io.Reader{}
	for _, chunk := range []string{"A\xe7", "\x95", "\x8cZ\r", "\nsecond", "\r", "\n"} {
		chunks = append(chunks, strings.NewReader(chunk))
	}
	br := bufio.NewReader(io.MultiReader(chunks...))
	for {
		err := p.vt.Parse(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	lines := r.History()
	if len(lines) != 1 || lines[0].text() != "A界Z" {
		t.Fatalf("history = %q, want one line A界Z", scrollbackTexts(r))
	}
	if cells := lines[0].cells; len(cells) != 4 || cells[1].Ch != '界' || cells[2].Ch != 0 || cells[3].Ch != 'Z' {
		t.Fatalf("history cells = %+v, want A, 界, wide placeholder, Z", cells)
	}
}

func TestResizePreservesScrollback(t *testing.T) {
	p := &pane{}
	p.initTerminal(10, 2)
	r := &p.sb
	writeScrollbackLines(p.vt, 3, "x")
	if got := scrollbackTexts(r); len(got) != 2 {
		t.Fatalf("history = %q, want two lines", got)
	}
	p.vt.Resize(4, 4)
	if got := scrollbackTexts(r); len(got) != 2 {
		t.Fatalf("resize changed history: %q", got)
	}
}

func TestCopyLineTextTrimsAndSkipsWidePlaceholders(t *testing.T) {
	blank := blankCopyCell()
	l := copyLine{cells: []copyCell{
		{Ch: 'h', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		{Ch: '界', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		{Ch: 0, FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		blank, blank,
	}}
	if got := l.text(); got != "h界" {
		t.Fatalf("text = %q, want h界", got)
	}
	if got := trimmedCopyLine(l); len(got.cells) != 3 {
		t.Fatalf("trimmed width = %d, want 3", len(got.cells))
	}
}

func TestScrollbackTopRegionAndHistoryControls(t *testing.T) {
	p := &pane{}
	p.initTerminal(10, 4)
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(p.vt, s); err != nil {
			t.Fatal(err)
		}
	}
	checkFirst := func() {
		t.Helper()
		if got := scrollbackTexts(&p.sb); len(got) != 1 || got[0] != "first" {
			t.Fatalf("history = %q, want only first", got)
		}
	}

	write("first\r\nsecond\r\nthird\r\nfourth")
	write("\x1b[1;2r\x1b[1S") // scroll only the top two rows
	checkFirst()
	write("\x1b[H\x1b[M") // delete at row zero
	checkFirst()
	write("\x1b[?1049h\x1b[3J\x1b[2S\x1b[?1049l")
	checkFirst()

	snapshot := p.copyBuffer()
	write("\x1b[3J")
	if len(p.sb.History()) != 0 || snapshot[0].text() != "first" {
		t.Fatal("clearing main-screen history should preserve the open copy buffer")
	}
}
