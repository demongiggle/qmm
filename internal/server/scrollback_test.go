package server

import (
	"fmt"
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

func writeScrollbackLines(r *scrollback, n int, prefix string) {
	for i := 1; i <= n; i++ {
		_, _ = r.Write([]byte(fmt.Sprintf("%s-%d\r\n", prefix, i)))
	}
}

func TestScrollbackKeepsLinesThatScrollOff(t *testing.T) {
	r := newScrollback(20, 4)
	writeScrollbackLines(r, 5, "line")
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
	r := newScrollback(20, 3)
	r.limit = 3
	writeScrollbackLines(r, 8, "ln")
	if got := strings.Join(scrollbackTexts(r), "|"); got != "ln-4|ln-5|ln-6" {
		t.Fatalf("history = %q, want last three scrolled lines", got)
	}
	_, _ = r.Write([]byte("\x1b[3J"))
	if got := scrollbackTexts(r); len(got) != 0 {
		t.Fatalf("CSI 3 J should clear history, got %q", got)
	}
}

func TestScrollbackIgnoresAlternateScreen(t *testing.T) {
	r := newScrollback(20, 3)
	_, _ = r.Write([]byte("\x1b[?1049h"))
	writeScrollbackLines(r, 6, "alt")
	if got := scrollbackTexts(r); len(got) != 0 {
		t.Fatalf("alternate screen redraws leaked into history: %q", got)
	}
	_, _ = r.Write([]byte("\x1b[?1049l"))
	writeScrollbackLines(r, 4, "main")
	if got := strings.Join(scrollbackTexts(r), "|"); got != "main-1|main-2" {
		t.Fatalf("history after alt screen = %q, want main-1", got)
	}
}

func TestScrollbackCSIAndSGR(t *testing.T) {
	r := newScrollback(20, 3)
	_, _ = r.Write([]byte("\x1b[1;31mbold red\x1b[0m\r\nplain\r\n"))
	_, _ = r.Write([]byte("\x1b[2S")) // scroll two more lines off the top
	lines := r.History()
	if len(lines) != 2 {
		t.Fatalf("history length = %d, want 2: %q", len(lines), scrollbackTexts(r))
	}
	if lines[0].text() != "bold red" {
		t.Fatalf("first history line = %q, want bold red", lines[0].text())
	}
	c := lines[0].cells[0]
	if c.FG != 1 || c.Style&protocol.StyleBold == 0 {
		t.Fatalf("first cell = %+v, want red foreground and bold", c)
	}
	if c = lines[1].cells[0]; c.FG != protocol.ColorDefault || c.Style != 0 {
		t.Fatalf("reset cell = %+v, want default style", c)
	}
}

func TestScrollbackResyncAndResize(t *testing.T) {
	r := newScrollback(10, 2)
	writeScrollbackLines(r, 3, "x")
	if got := scrollbackTexts(r); len(got) != 2 {
		t.Fatalf("history = %q, want two lines", got)
	}
	lines := []copyLine{
		{cells: []copyCell{{Ch: 'a', FG: protocol.ColorDefault, BG: protocol.ColorDefault}}},
		{cells: []copyCell{{Ch: 'b', FG: protocol.ColorDefault, BG: protocol.ColorDefault}}},
	}
	r.SetScreen(lines, 0, 1)
	if r.screen[1].cells[0].Ch != 'b' || r.y != 1 {
		t.Fatalf("resync did not install vt screen: %+v", r.screen)
	}
	r.Resize(4, 4)
	if r.rows != 4 || len(r.screen) != 4 || len(r.screen[0].cells) != 4 {
		t.Fatalf("resize = %dx%d, screen rows=%d cols=%d", r.cols, r.rows, len(r.screen), len(r.screen[0].cells))
	}
	if got := scrollbackTexts(r); len(got) != 2 {
		t.Fatalf("resize changed history: %q", got)
	}
}

func TestCopyLineTextTrimsAndSkipsWidePlaceholders(t *testing.T) {
	blank := blankCopyCell()
	l := copyLine{cells: []copyCell{
		{Ch: 'h', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		{Ch: 'i', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		blank, blank,
		{Ch: 0, FG: protocol.ColorDefault, BG: protocol.ColorDefault},
	}}
	if got := l.text(); got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
	if got := trimmedCopyLine(l); len(got.cells) != 2 {
		t.Fatalf("trimmed width = %d, want 2", len(got.cells))
	}
}
