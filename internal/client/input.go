package client

import (
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// encodeKey converts a tcell key event into the byte sequence to send to the
// pane (xterm-style).
func encodeKey(ev *tcell.EventKey) []byte {
	mods := ev.Modifiers()
	alt := mods&tcell.ModAlt != 0
	ctrl := mods&tcell.ModCtrl != 0
	key := ev.Key()

	emit := func(b []byte) []byte {
		if alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}

	switch key {
	case tcell.KeyEnter:
		return emit([]byte{'\r'})
	case tcell.KeyTab:
		return emit([]byte{'\t'})
	case tcell.KeyEscape:
		return []byte{0x1b}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if ctrl {
			return []byte{0x08}
		}
		return []byte{0x7f}
	case tcell.KeyUp:
		return emit(arrowSeq('A', ctrl))
	case tcell.KeyDown:
		return emit(arrowSeq('B', ctrl))
	case tcell.KeyRight:
		return emit(arrowSeq('C', ctrl))
	case tcell.KeyLeft:
		return emit(arrowSeq('D', ctrl))
	case tcell.KeyHome:
		if ctrl {
			return []byte("\x1b[1;5H")
		}
		return []byte("\x1b[H")
	case tcell.KeyEnd:
		if ctrl {
			return []byte("\x1b[1;5F")
		}
		return []byte("\x1b[F")
	case tcell.KeyPgUp:
		return []byte("\x1b[5~")
	case tcell.KeyPgDn:
		return []byte("\x1b[6~")
	case tcell.KeyInsert:
		return []byte("\x1b[2~")
	case tcell.KeyDelete:
		return []byte("\x1b[3~")
	case tcell.KeyF1:
		return []byte("\x1bOP")
	case tcell.KeyF2:
		return []byte("\x1bOQ")
	case tcell.KeyF3:
		return []byte("\x1bOR")
	case tcell.KeyF4:
		return []byte("\x1bOS")
	case tcell.KeyF5:
		return []byte("\x1b[15~")
	case tcell.KeyF6:
		return []byte("\x1b[17~")
	case tcell.KeyF7:
		return []byte("\x1b[18~")
	case tcell.KeyF8:
		return []byte("\x1b[19~")
	case tcell.KeyF9:
		return []byte("\x1b[20~")
	case tcell.KeyF10:
		return []byte("\x1b[21~")
	case tcell.KeyF11:
		return []byte("\x1b[23~")
	case tcell.KeyF12:
		return []byte("\x1b[24~")
	}

	// Ctrl+letter arrives either as a dedicated KeyCtrlX constant or as
	// KeyRune with the control modifier, depending on the terminal.
	if key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ {
		return []byte{byte(key-tcell.KeyCtrlA) + 1}
	}
	if key == tcell.KeyRune {
		r := ev.Rune()
		if ctrl && r >= 'a' && r <= 'z' {
			b := []byte{byte(r) - 'a' + 1}
			if alt {
				return append([]byte{0x1b}, b...)
			}
			return b
		}
		if ctrl && r >= 'A' && r <= 'Z' {
			b := []byte{byte(r) - 'A' + 1}
			if alt {
				return append([]byte{0x1b}, b...)
			}
			return b
		}
		b := make([]byte, utf8.RuneLen(r))
		utf8.EncodeRune(b, r)
		if alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}
	return nil
}

func arrowSeq(final byte, ctrl bool) []byte {
	if ctrl {
		return []byte{0x1b, '[', '1', ';', '5', final}
	}
	return []byte{0x1b, '[', final}
}
