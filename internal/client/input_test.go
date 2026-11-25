package client

import (
	"bytes"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func key(r rune, k tcell.Key, mods tcell.ModMask) *tcell.EventKey {
	return tcell.NewEventKey(k, r, mods)
}

func TestEncodeKey(t *testing.T) {
	cases := []struct {
		name string
		k    *tcell.EventKey
		want []byte
	}{
		{"rune", key('a', tcell.KeyRune, tcell.ModNone), []byte("a")},
		{"utf8 rune", key('€', tcell.KeyRune, tcell.ModNone), []byte("€")},
		{"ctrl-a (KeyRune)", key('a', tcell.KeyRune, tcell.ModCtrl), []byte{0x01}},
		{"ctrl-a (KeyCtrlA)", key(0, tcell.KeyCtrlA, tcell.ModCtrl), []byte{0x01}},
		{"ctrl-shift-a", key('A', tcell.KeyRune, tcell.ModCtrl|tcell.ModShift), []byte{0x01}},
		{"alt-a", key('a', tcell.KeyRune, tcell.ModAlt), []byte{0x1b, 'a'}},
		{"alt-ctrl-a", key('a', tcell.KeyRune, tcell.ModAlt|tcell.ModCtrl), []byte{0x1b, 0x01}},
		{"enter", key(0, tcell.KeyEnter, tcell.ModNone), []byte("\r")},
		{"alt-enter", key(0, tcell.KeyEnter, tcell.ModAlt), []byte{0x1b, '\r'}},
		{"tab", key(0, tcell.KeyTab, tcell.ModNone), []byte("\t")},
		{"escape", key(0, tcell.KeyEscape, tcell.ModNone), []byte{0x1b}},
		{"alt-escape stays single esc", key(0, tcell.KeyEscape, tcell.ModAlt), []byte{0x1b}},
		{"backspace", key(0, tcell.KeyBackspace, tcell.ModNone), []byte{0x7f}},
		{"ctrl-backspace", key(0, tcell.KeyBackspace, tcell.ModCtrl), []byte{0x08}},
		{"alt-backspace", key(0, tcell.KeyBackspace, tcell.ModAlt), []byte{0x1b, 0x7f}},
		{"up", key(0, tcell.KeyUp, tcell.ModNone), []byte("\x1b[A")},
		{"ctrl-up", key(0, tcell.KeyUp, tcell.ModCtrl), []byte("\x1b[1;5A")},
		{"alt-up", key(0, tcell.KeyUp, tcell.ModAlt), []byte("\x1b[1;3A")},
		{"ctrl-alt-up", key(0, tcell.KeyUp, tcell.ModCtrl|tcell.ModAlt), []byte("\x1b[1;7A")},
		{"left", key(0, tcell.KeyLeft, tcell.ModNone), []byte("\x1b[D")},
		{"home", key(0, tcell.KeyHome, tcell.ModNone), []byte("\x1b[H")},
		{"ctrl-home", key(0, tcell.KeyHome, tcell.ModCtrl), []byte("\x1b[1;5H")},
		{"end", key(0, tcell.KeyEnd, tcell.ModNone), []byte("\x1b[F")},
		{"pgup", key(0, tcell.KeyPgUp, tcell.ModNone), []byte("\x1b[5~")},
		{"ctrl-pgup", key(0, tcell.KeyPgUp, tcell.ModCtrl), []byte("\x1b[5;5~")},
		{"alt-pgup", key(0, tcell.KeyPgUp, tcell.ModAlt), []byte("\x1b[5;3~")},
		{"pgdn", key(0, tcell.KeyPgDn, tcell.ModNone), []byte("\x1b[6~")},
		{"delete", key(0, tcell.KeyDelete, tcell.ModNone), []byte("\x1b[3~")},
		{"insert", key(0, tcell.KeyInsert, tcell.ModNone), []byte("\x1b[2~")},
		{"f1", key(0, tcell.KeyF1, tcell.ModNone), []byte("\x1bOP")},
		{"f2", key(0, tcell.KeyF2, tcell.ModNone), []byte("\x1bOQ")},
		{"ctrl-f1", key(0, tcell.KeyF1, tcell.ModCtrl), []byte("\x1b[1;5P")},
		{"f5", key(0, tcell.KeyF5, tcell.ModNone), []byte("\x1b[15~")},
		{"f12", key(0, tcell.KeyF12, tcell.ModNone), []byte("\x1b[24~")},
		{"ctrl-f5", key(0, tcell.KeyF5, tcell.ModCtrl), []byte("\x1b[15;5~")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeKey(tc.k)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("encodeKey = %q, want %q", got, tc.want)
			}
		})
	}
}
