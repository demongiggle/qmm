package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// windowGotoTimeout is how long an ambiguous window number waits for another
// digit. A first digit that is also the prefix of a longer existing window
// index is ambiguous, so prefix+1 can still become window 10. This mirrors the
// pane-number input behind prefix+q: the number commits on Enter or when the
// shared ambiguity timeout expires.
const windowGotoTimeout = ambiguousDigitTimeout

// openWindowGoto begins a numeric window jump (prefix+0..9). The first digit
// is appended immediately, so single-digit windows switch without a delay.
func (s *Server) openWindowGoto(c *clientConn, digit int) {
	if s.sess == nil || len(s.sess.windows) == 0 {
		return
	}
	c.windowGotoOpen = true
	c.windowGotoInput = ""
	c.windowGotoDeadline = time.Time{}
	c.clearRepeat()
	s.appendWindowGotoDigit(c, digit)
}

// appendWindowGotoDigit adds one digit. It commits once the number is complete,
// or arms the timeout while a longer window index still starts with it.
func (s *Server) appendWindowGotoDigit(c *clientConn, digit int) {
	next := c.windowGotoInput + strconv.Itoa(digit)
	if s.sess == nil || !s.sess.windowNumberHasPrefix(next) {
		c.enqueue(protocol.Notice{Text: "qmm: invalid window number"})
		s.closeWindowGoto(c)
		return
	}
	c.windowGotoInput = next
	if s.sess.windowNumberHasLongerMatch(next) {
		c.windowGotoDeadline = time.Now().Add(windowGotoTimeout)
		return
	}
	s.commitWindowGoto(c)
}

// commitWindowGoto selects the typed window, or reports a stale number when
// that window disappeared while the number was being entered.
func (s *Server) commitWindowGoto(c *clientConn) {
	if !c.windowGotoOpen {
		return
	}
	input := c.windowGotoInput
	s.closeWindowGoto(c)
	n, err := strconv.Atoi(input)
	if err != nil || s.sess == nil || s.sess.windows[n] == nil {
		c.enqueue(protocol.Notice{Text: "qmm: invalid window number"})
		return
	}
	s.selectWindow(n)
}

func (s *Server) closeWindowGoto(c *clientConn) {
	c.windowGotoOpen = false
	c.windowGotoInput = ""
	c.windowGotoDeadline = time.Time{}
}

func (s *Server) windowGotoExpired(c *clientConn) bool {
	return c.windowGotoOpen && !c.windowGotoDeadline.IsZero() && !time.Now().Before(c.windowGotoDeadline)
}

// expireWindowGoto commits every pending window number whose ambiguity window
// has elapsed. Committing on timeout is what makes prefix+1 followed by a pause
// mean window 1 even when window 10 also exists.
func (s *Server) expireWindowGoto() {
	for c := range s.clients {
		if s.windowGotoExpired(c) {
			s.commitWindowGoto(c)
		}
	}
}

// consumeWindowGotoInput handles one key while a numeric jump is pending. It
// returns false when the key is outside the number, after committing the typed
// prefix, so the caller can process that key as ordinary input. The whole
// remaining slice is examined so an escape sequence is forwarded intact rather
// than losing its introducer to a cancel.
func (s *Server) consumeWindowGotoInput(c *clientConn, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	b := data[0]
	switch {
	case b >= '0' && b <= '9':
		s.appendWindowGotoDigit(c, int(b-'0'))
	case b == '\r' || b == '\n':
		s.commitWindowGoto(c)
	case b == 0x7f || b == 0x08:
		if len(c.windowGotoInput) > 0 {
			c.windowGotoInput = c.windowGotoInput[:len(c.windowGotoInput)-1]
		}
		if c.windowGotoInput == "" {
			s.closeWindowGoto(c)
		} else {
			c.windowGotoDeadline = time.Now().Add(windowGotoTimeout)
		}
	case b == 0x03 || (b == 0x1b && len(data) == 1):
		s.closeWindowGoto(c)
	default:
		s.commitWindowGoto(c)
		return false
	}
	return true
}

// windowNumberHasPrefix reports whether any window index starts with prefix.
func (s *session) windowNumberHasPrefix(prefix string) bool {
	for i := range s.windows {
		if strings.HasPrefix(strconv.Itoa(i), prefix) {
			return true
		}
	}
	return false
}

// windowNumberHasLongerMatch reports whether a longer window index also starts
// with prefix, making the typed number ambiguous.
func (s *session) windowNumberHasLongerMatch(prefix string) bool {
	for i := range s.windows {
		n := strconv.Itoa(i)
		if n != prefix && strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}
