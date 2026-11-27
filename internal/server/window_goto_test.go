package server

import (
	"strconv"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/vt10x"
)

// addTestWindow gives the session another window so numeric jumps have
// somewhere to go. Window indices are sparse, like after closing windows.
func addTestWindow(t *testing.T, s *Server, index int) {
	t.Helper()
	id := 100 + index
	s.sess.windows[index] = &window{
		index:      index,
		tree:       layout.New(id),
		activePane: id,
		name:       "w" + strconv.Itoa(index),
	}
	s.sess.panes[id] = &pane{id: id, vt: vt10x.New(vt10x.WithSize(39, 23))}
}

func TestWindowGotoSingleDigitSelectsImmediately(t *testing.T) {
	for d := '0'; d <= '9'; d++ {
		if !isExactBinding(string(d)) {
			t.Fatalf("prefix %c is not bound", d)
		}
	}
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)

	s.handleInput(c, []byte("\x011"))
	if c.windowGotoOpen {
		t.Fatalf("single-digit jump stayed open: input=%q", c.windowGotoInput)
	}
	if s.sess.active != 1 {
		t.Fatalf("active window = %d, want 1", s.sess.active)
	}
}

func TestWindowGotoAmbiguousDigitWaitsForSecond(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	// "1" could be window 1 or the start of window 10, so it waits.
	s.handleInput(c, []byte("\x011"))
	if !c.windowGotoOpen || c.windowGotoInput != "1" {
		t.Fatalf("ambiguous first digit = open:%v input:%q, want true/\"1\"", c.windowGotoOpen, c.windowGotoInput)
	}
	if s.sess.active != 0 {
		t.Fatalf("active window changed early: %d", s.sess.active)
	}

	s.handleInput(c, []byte("0"))
	if c.windowGotoOpen {
		t.Fatalf("two-digit jump stayed open: input=%q", c.windowGotoInput)
	}
	if s.sess.active != 10 {
		t.Fatalf("active window = %d, want 10", s.sess.active)
	}
	if s.sess.last != 0 {
		t.Fatalf("last window = %d, want 0", s.sess.last)
	}
}

func TestWindowGotoDigitsInOneInputMessage(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	// The second digit can arrive in the same client message as prefix+first.
	s.handleInput(c, []byte("\x0110"))
	if c.windowGotoOpen || s.sess.active != 10 {
		t.Fatalf("same-message jump = open:%v active:%d, want false/10", c.windowGotoOpen, s.sess.active)
	}
}

func TestWindowGotoTimeoutCommitsFirstDigit(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	s.handleInput(c, []byte("\x011"))
	c.windowGotoDeadline = time.Now().Add(-time.Millisecond)
	s.expireWindowGoto()
	if c.windowGotoOpen {
		t.Fatalf("timed-out jump stayed open: input=%q", c.windowGotoInput)
	}
	if s.sess.active != 1 {
		t.Fatalf("active window after timeout = %d, want 1", s.sess.active)
	}
}

func TestWindowGotoEnterCommitsAmbiguousInput(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	s.handleInput(c, []byte("\x011"))
	s.handleInput(c, []byte("\r"))
	if c.windowGotoOpen || s.sess.active != 1 {
		t.Fatalf("Enter jump = open:%v active:%d, want false/1", c.windowGotoOpen, s.sess.active)
	}
}

func TestWindowGotoCancelsAndRejectsInvalidNumbers(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	// Backspacing the only digit cancels the jump.
	s.handleInput(c, []byte("\x011\x7f"))
	if c.windowGotoOpen || s.sess.active != 0 {
		t.Fatalf("backspace cancel = open:%v active:%d, want false/0", c.windowGotoOpen, s.sess.active)
	}
	// Esc also cancels.
	s.handleInput(c, []byte("\x011\x1b"))
	if c.windowGotoOpen || s.sess.active != 0 {
		t.Fatalf("Esc cancel = open:%v active:%d, want false/0", c.windowGotoOpen, s.sess.active)
	}
	// A digit with no matching window rings the bell and stays put.
	s.handleInput(c, []byte("\x019"))
	if c.windowGotoOpen || s.sess.active != 0 {
		t.Fatalf("invalid jump = open:%v active:%d, want false/0", c.windowGotoOpen, s.sess.active)
	}
	foundNotice := false
	for _, msg := range c.queue {
		if _, ok := msg.(protocol.Notice); ok {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatal("invalid window number did not enqueue a notice")
	}
}

func TestWindowGotoNonDigitCommitsThenPassesThrough(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	s.handleInput(c, []byte("\x011"))
	// A new prefix is not part of the number: commit window 1, then let the
	// prefix start a fresh chord that cycles to the next window.
	s.handleInput(c, []byte("\x01n"))
	if c.windowGotoOpen {
		t.Fatalf("pending jump survived a non-digit: input=%q", c.windowGotoInput)
	}
	if s.sess.active != 10 {
		t.Fatalf("active window = %d, want 10 after commit plus cycle", s.sess.active)
	}
}

func TestWindowGotoEscapeSequenceCommitsInsteadOfCancelling(t *testing.T) {
	s, c := newHelpServer(80, 24)
	addTestWindow(t, s, 1)
	addTestWindow(t, s, 10)

	s.handleInput(c, []byte("\x011"))
	// Arrow keys start with ESC. Only a lone ESC cancels; a full sequence is
	// outside the number, so the prefix commits and the sequence passes on.
	if consumed := s.consumeWindowGotoInput(c, []byte("\x1b[A")); consumed {
		t.Fatal("escape sequence was consumed as a cancel")
	}
	if c.windowGotoOpen || s.sess.active != 1 {
		t.Fatalf("escape-sequence commit = open:%v active:%d, want false/1", c.windowGotoOpen, s.sess.active)
	}
}
