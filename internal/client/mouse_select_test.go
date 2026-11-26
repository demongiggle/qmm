package client

import (
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// expectNoMessage fails the test if any message arrives within a short wait.
// The client sends synchronously from handleMouse, so anything queued would
// surface almost immediately; the sleep only guards against scheduling lag.
func (f *mouseFixture) expectNoMessage(d time.Duration) {
	f.t.Helper()
	time.Sleep(d)
	select {
	case msg := <-f.msgs:
		f.t.Fatalf("unexpected message after gesture: %#v", msg)
	default:
	}
}

func TestDragInsidePaneSelectsAndExtends(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(10, 5) // inside the left pane
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok || msg.X != 10 || msg.Y != 5 {
		t.Fatalf("press = %#v, want ActionFocus{10,5}", msg)
	}
	f.dragTo(10, 5) // same cell: still a click, not a selection
	f.expectNoMessage(30 * time.Millisecond)

	f.dragTo(14, 5) // pointer left the press cell: selection starts
	if msg, ok := f.nextAction().(protocol.ActionSelectStart); !ok || msg.X != 10 || msg.Y != 5 {
		t.Fatalf("drag start = %#v, want ActionSelectStart{10,5}", msg)
	}
	if msg, ok := f.nextAction().(protocol.ActionSelectUpdate); !ok || msg.X != 14 || msg.Y != 5 {
		t.Fatalf("drag update = %#v, want ActionSelectUpdate{14,5}", msg)
	}

	f.dragTo(16, 8)
	if msg, ok := f.nextAction().(protocol.ActionSelectUpdate); !ok || msg.X != 16 || msg.Y != 8 {
		t.Fatalf("drag update = %#v, want ActionSelectUpdate{16,8}", msg)
	}

	f.release(16, 8)
	f.expectNoMessage(30 * time.Millisecond)
}

func TestDragWithNoCellChangeKeepsClickBehavior(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(10, 5)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok {
		t.Fatalf("press = %#v, want ActionFocus", msg)
	}
	f.dragTo(10, 5)
	f.release(10, 5)
	f.expectNoMessage(30 * time.Millisecond)
}

func TestDragOnSplitterDoesNotSelect(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(40, 5) // on the splitter
	if msg, ok := f.nextAction().(protocol.ActionDragStart); !ok {
		t.Fatalf("press = %#v, want ActionDragStart", msg)
	}
	f.dragTo(45, 5)
	if msg, ok := f.nextAction().(protocol.ActionDragUpdate); !ok {
		t.Fatalf("splitter drag = %#v, want ActionDragUpdate", msg)
	}
	f.release(45, 5)
	if msg, ok := f.nextAction().(protocol.ActionDragEnd); !ok {
		t.Fatalf("release = %#v, want ActionDragEnd", msg)
	}
	f.expectNoMessage(30 * time.Millisecond)
}

func TestDragOnStatusBarDoesNotSelect(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(10, 23) // status row: tab click
	if msg, ok := f.nextAction().(protocol.ActionSelectWindow); !ok {
		t.Fatalf("press = %#v, want ActionSelectWindow", msg)
	}
	f.dragTo(12, 23)
	f.expectNoMessage(30 * time.Millisecond)
	f.release(12, 23)
	f.expectNoMessage(30 * time.Millisecond)
}

func TestDragDoesNotBreakDoubleClickZoom(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(60, 10)
	if msg, ok := f.nextAction().(protocol.ActionFocus); !ok {
		t.Fatalf("first press = %#v, want ActionFocus", msg)
	}
	f.release(60, 10)
	f.press(60, 10) // quick second press: still a double-click, not a selection
	if msg, ok := f.nextAction().(protocol.ActionTogglePaneZoom); !ok {
		t.Fatalf("second press = %#v, want ActionTogglePaneZoom", msg)
	}
	f.release(60, 10)
	f.expectNoMessage(30 * time.Millisecond)
}

func TestDragSelectionStartsOnlyAfterLeavingPressCell(t *testing.T) {
	f := newMouseFixture(t, 80, 24, testFrame(80, 24))

	f.press(20, 7)
	_ = f.nextAction() // ActionFocus
	f.dragTo(20, 7)    // same cell
	f.expectNoMessage(30 * time.Millisecond)
	f.dragTo(21, 7) // one cell over: selection begins
	if msg, ok := f.nextAction().(protocol.ActionSelectStart); !ok || msg.X != 20 || msg.Y != 7 {
		t.Fatalf("start = %#v, want ActionSelectStart{20,7}", msg)
	}
	if msg, ok := f.nextAction().(protocol.ActionSelectUpdate); !ok || msg.X != 21 || msg.Y != 7 {
		t.Fatalf("update = %#v, want ActionSelectUpdate{21,7}", msg)
	}
}
