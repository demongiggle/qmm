package client

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/qmm/internal/protocol"
)

// clipboardTty records writes without needing a real terminal. Only Write is
// used by screenHandle.writeClipboard; the embedded interface covers the rest.
type clipboardTty struct {
	tcell.Tty
	buf bytes.Buffer
}

func (t *clipboardTty) Write(p []byte) (int, error) {
	return t.buf.Write(p)
}

func TestWriteClipboardEmitsOSC52(t *testing.T) {
	tty := &clipboardTty{}
	h := &screenHandle{tty: tty}
	text := "copied text\nwith ünicode & <tags>"
	if err := h.writeClipboard(text); err != nil {
		t.Fatalf("writeClipboard: %v", err)
	}
	got := tty.buf.String()
	const prefix = "\x1b]52;c;"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "\x07") {
		t.Fatalf("OSC 52 framing = %q", got)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(got, prefix), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload %q: %v", payload, err)
	}
	if string(decoded) != text {
		t.Fatalf("clipboard payload = %q, want %q", decoded, text)
	}
}

func TestWheelEventsSendActionWheel(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.attach.handleMouse(tcell.NewEventMouse(5, 5, tcell.WheelUp, tcell.ModNone))
	if msg, ok := f.nextAction().(protocol.ActionWheel); !ok || msg.Delta != -1 {
		t.Fatalf("wheel up = %#v, want ActionWheel{Delta:-1}", msg)
	}
	f.attach.handleMouse(tcell.NewEventMouse(5, 5, tcell.WheelDown, tcell.ModNone))
	if msg, ok := f.nextAction().(protocol.ActionWheel); !ok || msg.Delta != 1 {
		t.Fatalf("wheel down = %#v, want ActionWheel{Delta:1}", msg)
	}
}
