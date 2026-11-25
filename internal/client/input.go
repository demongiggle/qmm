package client

import (
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// encodeKey converts a tcell key event into the byte sequence to send to the
// pane (xterm-style). Ctrl and Alt map onto the CSI modifier parameter where
// the sequence family supports it (xterm modifier: 2=shift, 3=alt, 4=shift+alt,
// 5=ctrl, 6=ctrl+shift, 7=alt+ctrl); otherwise Alt prefixes ESC.
func encodeKey(ev *tcell.EventKey) []byte {
	mods := ev.Modifiers()
	ctrl := mods&tcell.ModCtrl != 0
	alt := mods&tcell.ModAlt != 0
	key := ev.Key()

	switch key {
	case tcell.KeyEnter:
		return withAlt([]byte{'\r'}, alt)
	case tcell.KeyTab:
		return withAlt([]byte{'\t'}, alt)
	case tcell.KeyEscape:
		return []byte{0x1b} // never double-wrapped
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if ctrl {
			return withAlt([]byte{0x08}, alt)
		}
		return withAlt([]byte{0x7f}, alt)
	case tcell.KeyUp:
		return csiFinal('A', ctrl, alt)
	case tcell.KeyDown:
		return csiFinal('B', ctrl, alt)
	case tcell.KeyRight:
		return csiFinal('C', ctrl, alt)
	case tcell.KeyLeft:
		return csiFinal('D', ctrl, alt)
	case tcell.KeyHome:
		return csiFinal('H', ctrl, alt)
	case tcell.KeyEnd:
		return csiFinal('F', ctrl, alt)
	case tcell.KeyPgUp:
		return csiTilde(5, ctrl, alt)
	case tcell.KeyPgDn:
		return csiTilde(6, ctrl, alt)
	case tcell.KeyInsert:
		return csiTilde(2, ctrl, alt)
	case tcell.KeyDelete:
		return csiTilde(3, ctrl, alt)
	case tcell.KeyF1:
		return ss3('P', ctrl, alt)
	case tcell.KeyF2:
		return ss3('Q', ctrl, alt)
	case tcell.KeyF3:
		return ss3('R', ctrl, alt)
	case tcell.KeyF4:
		return ss3('S', ctrl, alt)
	case tcell.KeyF5:
		return csiTilde(15, ctrl, alt)
	case tcell.KeyF6:
		return csiTilde(17, ctrl, alt)
	case tcell.KeyF7:
		return csiTilde(18, ctrl, alt)
	case tcell.KeyF8:
		return csiTilde(19, ctrl, alt)
	case tcell.KeyF9:
		return csiTilde(20, ctrl, alt)
	case tcell.KeyF10:
		return csiTilde(21, ctrl, alt)
	case tcell.KeyF11:
		return csiTilde(23, ctrl, alt)
	case tcell.KeyF12:
		return csiTilde(24, ctrl, alt)
	}
	switch {
	case key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ:
		return withAlt([]byte{byte(key-tcell.KeyCtrlA) + 1}, alt)
	case key == tcell.KeyRune:
		r := ev.Rune()
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
// 4=shift+alt, 5=ctrl, 6=ctrl+shift, 7=alt+ctrl.
func modParam(ctrl, alt bool) int {
	m := 1
	if alt {
		m += 2
	}
	if ctrl {
		m += 4
	}
	return m
}

func csiFinal(final byte, ctrl, alt bool) []byte {
	m := modParam(ctrl, alt)
	if m == 1 {
		return []byte{0x1b, '[', final}
	}
	return []byte("\x1b[1;" + strconv.Itoa(m) + string(final))
}

func csiTilde(n int, ctrl, alt bool) []byte {
	m := modParam(ctrl, alt)
	if m == 1 {
		return []byte("\x1b[" + strconv.Itoa(n) + "~")
	}
	return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(m) + "~")
}

func ss3(letter byte, ctrl, alt bool) []byte {
	m := modParam(ctrl, alt)
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
