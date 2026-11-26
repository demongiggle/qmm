package client

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/qmm/internal/protocol"
)

type fakeTty struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed chan struct{}
	cols   int
	rows   int
}

func newFakeTty(cols, rows int) *fakeTty {
	return &fakeTty{
		closed: make(chan struct{}),
		cols:   cols,
		rows:   rows,
	}
}

func (f *fakeTty) Start() error { return nil }
func (f *fakeTty) Stop() error  { return nil }
func (f *fakeTty) Drain() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}
func (f *fakeTty) NotifyResize(func()) {}
func (f *fakeTty) WindowSize() (tcell.WindowSize, error) {
	return tcell.WindowSize{Width: f.cols, Height: f.rows}, nil
}
func (f *fakeTty) Read(p []byte) (int, error) {
	<-f.closed
	return 0, io.EOF
}
func (f *fakeTty) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *fakeTty) Close() error { return f.Drain() }

func (f *fakeTty) take() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]byte(nil), f.buf.Bytes()...)
	f.buf.Reset()
	return out
}

func TestSteadyCursorTtySuppressesPerFrameCursorToggle(t *testing.T) {
	ti, err := tcell.LookupTerminfo("xterm-256color")
	if err != nil {
		t.Fatalf("LookupTerminfo(xterm-256color): %v", err)
	}
	var hideBuf, showBuf bytes.Buffer
	ti.TPuts(&hideBuf, ti.HideCursor)
	ti.TPuts(&showBuf, ti.ShowCursor)
	hideSeq := hideBuf.Bytes()
	showSeq := showBuf.Bytes()
	if len(hideSeq) == 0 || len(showSeq) == 0 {
		t.Fatalf("unexpected empty cursor sequences in xterm-256color")
	}

	raw := newFakeTty(10, 2)
	tty := newSteadyCursorTty(raw, ti)
	screen, err := tcell.NewTerminfoScreenFromTtyTerminfo(tty, ti)
	if err != nil {
		t.Fatalf("NewTerminfoScreenFromTtyTerminfo: %v", err)
	}
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}
	screen.HideCursor()
	screen.Clear()
	screen.Show()
	raw.take() // discard startup setup

	a := &attach{screen: screen}

	// First visible steady frame shows the cursor and sets steady block once.
	a.applyFrame(protocol.Frame{
		Full: true, Cols: 10, Rows: 2,
		Cells: []protocol.Cell{
			{Row: 0, Col: 0, Ch: 'A', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
			{Row: 1, Col: 9, Ch: '0', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		},
		CursorX: 0, CursorY: 0, CursorVisible: true,
		CursorShape: protocol.CursorShapeBlock, CursorBlink: false,
	})
	screen.Show()
	out := raw.take()
	if bytes.Contains(out, hideSeq) {
		t.Errorf("first visible frame emitted redundant HideCursor: %q", out)
	}
	if !bytes.Contains(out, showSeq) {
		t.Errorf("first visible frame did not emit ShowCursor: %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[2 q")) {
		t.Errorf("first visible frame did not emit steady block DECSCUSR: %q", out)
	}

	// Subsequent frames updating other cells (like the 1 Hz status clock or fast
	// pane output) must NOT emit HideCursor, ShowCursor, or redundant DECSCUSR.
	for i, ch := range []rune{'1', '2', '3'} {
		a.applyFrame(protocol.Frame{
			Cols: 10, Rows: 2,
			Cells: []protocol.Cell{
				{Row: 1, Col: 9, Ch: ch, FG: protocol.ColorDefault, BG: protocol.ColorDefault},
			},
			CursorX: 0, CursorY: 0, CursorVisible: true,
			CursorShape: protocol.CursorShapeBlock, CursorBlink: false,
		})
		screen.Show()
		out = raw.take()
		if bytes.Contains(out, hideSeq) {
			t.Errorf("steady frame %d emitted HideCursor: %q", i, out)
		}
		if bytes.Contains(out, showSeq) {
			t.Errorf("steady frame %d emitted ShowCursor: %q", i, out)
		}
		if bytes.Contains(out, []byte("\x1b[2 q")) {
			t.Errorf("steady frame %d emitted redundant DECSCUSR: %q", i, out)
		}
	}

	// Changing cursor shape emits the new DECSCUSR once, without hiding/showing.
	a.applyFrame(protocol.Frame{
		Cols: 10, Rows: 2,
		CursorX: 0, CursorY: 0, CursorVisible: true,
		CursorShape: protocol.CursorShapeBar, CursorBlink: false,
	})
	screen.Show()
	out = raw.take()
	if bytes.Contains(out, hideSeq) || bytes.Contains(out, showSeq) {
		t.Errorf("shape change while visible toggled cursor visibility: %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[6 q")) {
		t.Errorf("shape change did not emit steady bar DECSCUSR: %q", out)
	}

	// Blinking OFF hides the cursor once; output while hidden does not re-emit hide.
	a.applyFrame(protocol.Frame{
		Cols: 10, Rows: 2,
		CursorX: 0, CursorY: 0, CursorVisible: true,
		CursorShape: protocol.CursorShapeBar, CursorBlink: true,
	})
	screen.Show()
	raw.take()

	if !a.blinkCaret() {
		t.Fatal("blinkCaret did not transition to off phase")
	}
	screen.Show()
	out = raw.take()
	if !bytes.Contains(out, hideSeq) {
		t.Errorf("blink-off did not emit HideCursor: %q", out)
	}
	if bytes.Contains(out, showSeq) {
		t.Errorf("blink-off emitted ShowCursor: %q", out)
	}

	a.applyFrame(protocol.Frame{
		Cols: 10, Rows: 2,
		Cells: []protocol.Cell{
			{Row: 1, Col: 9, Ch: '4', FG: protocol.ColorDefault, BG: protocol.ColorDefault},
		},
		CursorX: 0, CursorY: 0, CursorVisible: true,
		CursorShape: protocol.CursorShapeBar, CursorBlink: true,
	})
	screen.Show()
	out = raw.take()
	if bytes.Contains(out, hideSeq) || bytes.Contains(out, showSeq) {
		t.Errorf("output during blink-off emitted cursor toggle: %q", out)
	}

	// Blinking back ON shows the cursor without repeating the unchanged DECSCUSR.
	if !a.blinkCaret() {
		t.Fatal("blinkCaret did not transition to on phase")
	}
	screen.Show()
	out = raw.take()
	if bytes.Contains(out, hideSeq) {
		t.Errorf("blink-on emitted HideCursor: %q", out)
	}
	if !bytes.Contains(out, showSeq) {
		t.Errorf("blink-on did not emit ShowCursor: %q", out)
	}
	if bytes.Contains(out, []byte("\x1b[6 q")) {
		t.Errorf("blink-on emitted redundant DECSCUSR: %q", out)
	}

	// Fini restores cursor visibility and default DECSCUSR style.
	screen.Fini()
	out = raw.take()
	if !bytes.Contains(out, showSeq) {
		t.Errorf("Fini did not restore ShowCursor: %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[0 q")) {
		t.Errorf("Fini did not restore default DECSCUSR: %q", out)
	}
}

func TestSteadyCursorTtyWithoutSyncOutputPrefix(t *testing.T) {
	raw := newFakeTty(10, 2)
	tty := &steadyCursorTty{
		Tty:     raw,
		hideSeq: []byte("\x1b[?25l"),
		showSeq: []byte("\x1b[?25h"),
	}

	// Init hides cursor.
	if _, err := tty.Write([]byte("\x1b[?25l")); err != nil {
		t.Fatal(err)
	}
	raw.take()

	// First draw shows cursor (no sync-output prefix).
	if _, err := tty.Write([]byte("\x1b[?25lhello\x1b[1;6H\x1b[?25h\x1b[2 q")); err != nil {
		t.Fatal(err)
	}
	if got := string(raw.take()); got != "hello\x1b[1;6H\x1b[?25h\x1b[2 q" {
		t.Fatalf("first draw = %q, want %q", got, "hello\x1b[1;6H\x1b[?25h\x1b[2 q")
	}

	// Second draw keeps cursor steady without hide/show/DECSCUSR.
	if _, err := tty.Write([]byte("\x1b[?25lworld\x1b[1;6H\x1b[?25h\x1b[2 q")); err != nil {
		t.Fatal(err)
	}
	if got := string(raw.take()); got != "world\x1b[1;6H" {
		t.Fatalf("second draw = %q, want %q", got, "world\x1b[1;6H")
	}
}
