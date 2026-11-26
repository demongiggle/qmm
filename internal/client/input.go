package client

import (
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// encodeKey converts a tcell key event into the byte sequence to send to the
// pane (xterm-style). Shift/Ctrl/Alt map onto the CSI modifier parameter
// where the sequence family supports it (2=shift, 3=alt, 4=shift+alt, 5=ctrl,
// 6=ctrl+shift, 7=alt+ctrl, 8=shift+alt+ctrl); otherwise Alt prefixes ESC.
func encodeKey(ev *tcell.EventKey) []byte {
	mods := ev.Modifiers()
	shift := mods&tcell.ModShift != 0
	ctrl := mods&tcell.ModCtrl != 0
	alt := mods&tcell.ModAlt != 0
	key := ev.Key()

	switch key {
	case tcell.KeyEnter:
		return withAlt([]byte{'\r'}, alt)
	case tcell.KeyTab:
		if shift {
			return withAlt([]byte("\x1b[Z"), alt) // backtab
		}
		return withAlt([]byte{'\t'}, alt)
	case tcell.KeyBacktab:
		return withAlt([]byte("\x1b[Z"), alt) // shift-tab
	case tcell.KeyEscape:
		return []byte{0x1b} // never double-wrapped
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if ctrl {
			return withAlt([]byte{0x08}, alt)
		}
		return withAlt([]byte{0x7f}, alt)
	case tcell.KeyUp:
		return csiFinal('A', shift, ctrl, alt)
	case tcell.KeyDown:
		return csiFinal('B', shift, ctrl, alt)
	case tcell.KeyRight:
		return csiFinal('C', shift, ctrl, alt)
	case tcell.KeyLeft:
		return csiFinal('D', shift, ctrl, alt)
	case tcell.KeyHome:
		return csiFinal('H', shift, ctrl, alt)
	case tcell.KeyEnd:
		return csiFinal('F', shift, ctrl, alt)
	case tcell.KeyPgUp:
		return csiTilde(5, shift, ctrl, alt)
	case tcell.KeyPgDn:
		return csiTilde(6, shift, ctrl, alt)
	case tcell.KeyInsert:
		return csiTilde(2, shift, ctrl, alt)
	case tcell.KeyDelete:
		return csiTilde(3, shift, ctrl, alt)
	case tcell.KeyF1:
		return ss3('P', shift, ctrl, alt)
	case tcell.KeyF2:
		return ss3('Q', shift, ctrl, alt)
	case tcell.KeyF3:
		return ss3('R', shift, ctrl, alt)
	case tcell.KeyF4:
		return ss3('S', shift, ctrl, alt)
	case tcell.KeyF5:
		return csiTilde(15, shift, ctrl, alt)
	case tcell.KeyF6:
		return csiTilde(17, shift, ctrl, alt)
	case tcell.KeyF7:
		return csiTilde(18, shift, ctrl, alt)
	case tcell.KeyF8:
		return csiTilde(19, shift, ctrl, alt)
	case tcell.KeyF9:
		return csiTilde(20, shift, ctrl, alt)
	case tcell.KeyF10:
		return csiTilde(21, shift, ctrl, alt)
	case tcell.KeyF11:
		return csiTilde(23, shift, ctrl, alt)
	case tcell.KeyF12:
		return csiTilde(24, shift, ctrl, alt)
	}
	switch {
	case key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ:
		return withAlt([]byte{byte(key-tcell.KeyCtrlA) + 1}, alt)
	case key == tcell.KeyRune:
		r := ev.Rune()
		if ctrl && shift && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			// Keep Ctrl-Shift-<letter> distinguishable from Ctrl-<letter> by
			// re-encoding it in xterm's modifyOtherKeys format. The server
			// matches this sequence for the configurable copy key.
			return []byte(fmt.Sprintf("\x1b[27;6;%d~", unicode.ToLower(r)))
		}
		if ctrl && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return withAlt([]byte{byte(unicode.ToLower(r)) - 'a' + 1}, alt)
		}
		b := make([]byte, utf8.RuneLen(r))
		utf8.EncodeRune(b, r)
		return withAlt(b, alt)
	}
	return nil
}

// modParam returns the xterm modifier parameter: 1=none, 2=shift, 3=alt,
// 4=shift+alt, 5=ctrl, 6=ctrl+shift, 7=alt+ctrl, 8=shift+alt+ctrl.
func modParam(shift, ctrl, alt bool) int {
	m := 1
	if shift {
		m += 1
	}
	if alt {
		m += 2
	}
	if ctrl {
		m += 4
	}
	return m
}

func csiFinal(final byte, shift, ctrl, alt bool) []byte {
	m := modParam(shift, ctrl, alt)
	if m == 1 {
		return []byte{0x1b, '[', final}
	}
	return []byte("\x1b[1;" + strconv.Itoa(m) + string(final))
}

func csiTilde(n int, shift, ctrl, alt bool) []byte {
	m := modParam(shift, ctrl, alt)
	if m == 1 {
		return []byte("\x1b[" + strconv.Itoa(n) + "~")
	}
	return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(m) + "~")
}

func ss3(letter byte, shift, ctrl, alt bool) []byte {
	m := modParam(shift, ctrl, alt)
	if m == 1 {
		return []byte{0x1b, 'O', letter}
	}
	return []byte("\x1b[1;" + strconv.Itoa(m) + string(letter))
}

func withAlt(seq []byte, alt bool) []byte {
	if !alt {
		return seq
	}
	out := make([]byte, 0, len(seq)+1)
	out = append(out, 0x1b)
	return append(out, seq...)
}
