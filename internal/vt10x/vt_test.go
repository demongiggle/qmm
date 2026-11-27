package vt10x

import (
	"io"
	"strings"
	"testing"
)

func extractStr(term Terminal, x0, x1, row int) string {
	var s []rune
	for i := x0; i <= x1; i++ {
		attr := term.Cell(i, row)
		s = append(s, attr.Char)
	}
	return string(s)
}

func TestPlainChars(t *testing.T) {
	term := New()
	expected := "Hello world!"
	_, err := term.Write([]byte(expected))
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	actual := extractStr(term, 0, len(expected)-1, 0)
	if expected != actual {
		t.Fatal(actual)
	}
}

func TestNewline(t *testing.T) {
	term := New()
	expected := "Hello world!\n...and more."
	_, err := term.Write([]byte("\033[20h")) // set CRLF mode
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	_, err = term.Write([]byte(expected))
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}

	split := strings.Split(expected, "\n")
	actual := extractStr(term, 0, len(split[0])-1, 0)
	actual += "\n"
	actual += extractStr(term, 0, len(split[1])-1, 1)
	if expected != actual {
		t.Fatal(actual)
	}

	// A newline with a color set should not make the next line that color,
	// which used to happen if it caused a scroll event.
	st := (term.(*terminal))
	st.moveTo(0, st.rows-1)
	_, err = term.Write([]byte("\033[1;37m\n$ \033[m"))
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	cur := term.Cursor()
	attr := term.Cell(cur.X, cur.Y)
	if attr.FG != DefaultFG {
		t.Fatal(st.cur.X, st.cur.Y, attr.FG, attr.BG)
	}
}

func TestNewlineScrollBCE(t *testing.T) {
	term := New(WithSize(20, 5))
	// Set 256-color background 235 and clear screen, then scroll down one line
	// inside a scrolling region via LF (matching vim's line-by-line scroll).
	seq := "\033[m\033[38;5;252m\033[48;5;235m\033[H\033[2J" +
		"\033[1;4r\033[4;1H\r\n\033[1;5r\033[4;1Habc"
	if _, err := term.Write([]byte(seq)); err != nil {
		t.Fatal(err)
	}
	for x := 0; x < 20; x++ {
		cell := term.Cell(x, 3)
		if cell.BG != Color(235) {
			t.Fatalf("cell(%d,3) BG = %d, want 235", x, cell.BG)
		}
		if cell.Mode != 0 {
			t.Fatalf("cell(%d,3) Mode = %d, want 0", x, cell.Mode)
		}
	}
}
