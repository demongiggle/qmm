package client

import (
	"net"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/demongiggle/cogito/internal/protocol"
)

// mouseFixture wires a client-side attach struct to an in-memory screen and
// captures everything the client sends to the "server" end of a pipe.
type mouseFixture struct {
	t      *testing.T
	attach *attach
	msgs   chan interface{}
	server *protocol.Codec
	conn   net.Conn
}

func newMouseFixture(t *testing.T, cols, rows int, frame protocol.Frame) *mouseFixture {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("sim screen: %v", err)
	}
	screen.SetSize(cols, rows)

	clientEnd, serverEnd := net.Pipe()
	clientCodec := protocol.NewCodec(clientEnd)
	serverCodec := protocol.NewCodec(serverEnd)

	f := &mouseFixture{t: t, server: serverCodec, conn: serverEnd, msgs: make(chan interface{}, 16)}
	f.attach = &attach{
		screen:    screen,
		codec:     clientCodec,
		cols:      cols,
		rows:      rows,
		frameCols: frame.Cols,
		frameRows: frame.Rows,
		frame:     frame,
	}
	// Drain the server side in the background; net.Pipe is unbuffered, so
	// the client would block on the first Send otherwise.
	go func() {
		for {
			msg, err := serverCodec.Recv()
			if err != nil {
				close(f.msgs)
				return
			}
			f.msgs <- msg
		}
	}()
	f.t.Cleanup(func() {
		screen.Fini()
		_ = serverEnd.Close()
		_ = clientEnd.Close()
	})
	return f
}

func (f *mouseFixture) press(x, y int) {
	f.attach.handleMouse(tcell.NewEventMouse(x, y, tcell.ButtonPrimary, tcell.ModNone))
}

func (f *mouseFixture) dragTo(x, y int) {
	f.attach.handleMouse(tcell.NewEventMouse(x, y, tcell.ButtonPrimary, tcell.ModNone))
}

func (f *mouseFixture) release(x, y int) {
	f.attach.handleMouse(tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone))
}

// nextAction returns the next message the client sent to the "server".
func (f *mouseFixture) nextAction() interface{} {
	f.t.Helper()
	select {
	case msg, ok := <-f.msgs:
		if !ok {
			f.t.Fatal("server side closed")
		}
		return msg
	case <-time.After(time.Second):
		f.t.Fatal("timed out waiting for client action")
		return nil
	}
}

func testFrame(cols, rows int) protocol.Frame {
	return protocol.Frame{
		Full: true, Cols: cols, Rows: rows,
		Tabs: []protocol.Tab{
			{Index: 0, Start: 0, End: 6, Name: "bash", Active: true},
			{Index: 3, Start: 8, End: 14, Name: "vim", Active: false},
		},
		Splitters: []protocol.Splitter{
			{ID: 42, X: 40, Y: 0, Len: rows - 1, Vertical: true},
		},
		Panes: []protocol.PaneRect{
			{ID: 1, X: 0, Y: 0, W: 40, H: rows - 1, Active: true},
			{ID: 2, X: 41, Y: 0, W: cols - 41, H: rows - 1},
		},
	}
}

func TestStatusBarClickSelectsWindow(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(10, 23) // inside tab 1 (cols 8-13), on the status row
	f.release(10, 23)
	if msg, ok := f.nextAction().(protocol.ActionSelectWindow); !ok || msg.Index != 3 {
		t.Fatalf("expected ActionSelectWindow{3}, got %#v", msg)
	}
}

func TestSplitterDragProducesDeltaStream(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(40, 5) // on the splitter
	if msg, ok := f.nextAction().(protocol.ActionDragStart); !ok || msg.Splitter != 42 {
		t.Fatalf("expected ActionDragStart{42}, got %#v", msg)
	}
	f.dragTo(45, 5)
	if msg, ok := f.nextAction().(protocol.ActionDragUpdate); !ok || msg.Splitter != 42 || msg.Delta != 5 {
		t.Fatalf("expected ActionDragUpdate{42, +5}, got %#v", msg)
	}
	f.dragTo(43, 5)
	if msg, ok := f.nextAction().(protocol.ActionDragUpdate); !ok || msg.Delta != -2 {
		t.Fatalf("expected ActionDragUpdate{Delta: -2}, got %#v", msg)
	}
	f.release(43, 5)
	if msg, ok := f.nextAction().(protocol.ActionDragEnd); !ok || msg.Splitter != 42 {
		t.Fatalf("expected ActionDragEnd{42}, got %#v", msg)
	}
}

func TestPaneClickFocuses(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(60, 10) // inside the right pane
	f.release(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok || msg.X != 60 || msg.Y != 10 {
		t.Fatalf("expected ActionFocus{60,10}, got %#v", msg)
	}
}

func TestClickNextToSplitterFocusesInsteadOfDragging(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(39, 10) // one cell left of the splitter: focus, not drag
	f.release(39, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok {
		t.Fatalf("expected ActionFocus, got %#v", msg)
	}
}

func TestDoubleClickPaneTogglesZoom(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(60, 10) // inside the right pane
	f.release(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok || msg.X != 60 || msg.Y != 10 {
		t.Fatalf("first click = %#v, want ActionFocus{60,10}", msg)
	}
	f.press(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionTogglePaneZoom); !ok || msg.PaneID != 2 {
		t.Fatalf("second click = %#v, want ActionTogglePaneZoom{2}", msg)
	}
	f.release(60, 10)
}

func TestDoubleClickUsesZoomedOverlayHitTarget(t *testing.T) {
	frame := testFrame(80, 24)
	frame.Panes = append(frame.Panes, protocol.PaneRect{
		ID: 9, X: 10, Y: 5, W: 60, H: 14, Active: true, Zoomed: true,
	})
	f := newMouseFixture(t, 80, 24, frame)

	// This point is also inside the left split pane; the floating overlay
	// must win so a double-click toggles the pane actually shown on top.
	f.press(20, 10)
	f.release(20, 10)
	_ = f.nextAction() // first click focuses
	f.press(20, 10)
	if msg, ok := f.nextAction().(protocol.ActionTogglePaneZoom); !ok || msg.PaneID != 9 {
		t.Fatalf("overlay double-click = %#v, want ActionTogglePaneZoom{9}", msg)
	}
}

func TestSlowSecondClickFocusesInsteadOfZooming(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(60, 10)
	f.release(60, 10)
	_ = f.nextAction()
	f.attach.lastClick.at = time.Now().Add(-2 * doubleClickInterval)

	f.press(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok || msg.X != 60 || msg.Y != 10 {
		t.Fatalf("slow second click = %#v, want ActionFocus{60,10}", msg)
	}
}

func TestDragClearsDoubleClickHistory(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))
	f.press(60, 10)
	f.release(60, 10)
	_ = f.nextAction()

	f.press(40, 5) // start a drag on the splitter
	if msg, ok := f.nextAction().(protocol.ActionDragStart); !ok || msg.Splitter != 42 {
		t.Fatalf("drag press = %#v, want ActionDragStart{42}", msg)
	}
	f.release(40, 5)
	_ = f.nextAction() // ActionDragEnd

	f.press(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok || msg.X != 60 || msg.Y != 10 {
		t.Fatalf("click after drag = %#v, want ActionFocus{60,10}", msg)
	}
}

func TestPressOutsideFrameIsIgnored(t *testing.T) {
	// Frame is smaller than the screen; clicks outside the frame are dropped.
	frame := testFrame(40, 12)
	f := newMouseFixture(t, 80, 24, frame)
	f.press(60, 20) // beyond frame size
	f.release(60, 20)
	select {
	case msg := <-f.msgs:
		t.Fatalf("expected no message, got %#v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}
