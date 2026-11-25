package protocol

import (
	"net"
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	msgs := []interface{}{
		Hello{Term: "xterm-256color", Cols: 120, Rows: 40},
		Input{Data: []byte{0x01, 'c', 0xff, 0x1b}},
		Resize{Cols: 100, Rows: 30},
		Detach{},
		Shutdown{},
		ActionFocus{X: 5, Y: 7},
		ActionSelectWindow{Index: 3},
		ActionNewWindow{},
		ActionSplit{Vertical: true},
		ActionKillPane{},
		ActionNextWindow{},
		ActionPrevWindow{},
		ActionCyclePane{},
		ActionToggleZoom{},
		ActionTogglePaneZoom{PaneID: 7},
		ActionResizePane{Direction: DirRight, Delta: 3},
		ActionDragStart{Splitter: 7},
		ActionDragUpdate{Splitter: 7, Delta: -2},
		ActionDragEnd{Splitter: 7},
		Welcome{Cols: 120, Rows: 40},
		Frame{
			Full: true, Cols: 2, Rows: 1,
			Cells: []Cell{
				{Row: 0, Col: 0, Ch: 'A', FG: 1, BG: 2, Style: StyleBold | StyleReverse},
				{Row: 0, Col: 1, Ch: 'B', FG: RGBColor(18, 52, 86), BG: ColorDefault},
			},
			CursorX: 1, CursorY: 0, CursorVisible: true,
			Panes:     []PaneRect{{ID: 1, X: 0, Y: 0, W: 2, H: 1, Active: true, Zoomed: true}},
			Splitters: []Splitter{{ID: 9, X: 1, Y: 0, Len: 1, Vertical: true}},
			Tabs:      []Tab{{Index: 0, Start: 0, End: 5, Name: "bash", Active: true, Zoomed: true}},
		},
		Notice{Text: "hello"},
		Goodbye{Reason: "detached"},
	}

	for i, want := range msgs {
		serverSide, clientSide := net.Pipe()
		defer serverSide.Close()
		defer clientSide.Close()

		senc, srecv := NewCodec(serverSide), NewCodec(serverSide)
		_ = srecv

		errCh := make(chan error, 1)
		go func() {
			defer clientSide.Close()
			bdec := NewCodec(clientSide)
			got, err := bdec.Recv()
			if err != nil {
				errCh <- err
				return
			}
			if !reflect.DeepEqual(got, want) {
				errCh <- &mismatchError{got: got, want: want}
				return
			}
			errCh <- nil
		}()

		if err := senc.Send(want); err != nil {
			t.Fatalf("msg %d: send: %v", i, err)
		}
		if err := <-errCh; err != nil {
			t.Fatalf("msg %d (%T): %v", i, want, err)
		}
	}
}

func TestRGBColor(t *testing.T) {
	want := ColorRGBFlag | 0x123456
	if got := RGBColor(0x12, 0x34, 0x56); got != want {
		t.Fatalf("RGBColor = %#x, want %#x", got, want)
	}
	// Components are bytes; out-of-range values must not spill into the flag.
	if got := RGBColor(0x112, 0x234, 0x356); got != want {
		t.Fatalf("RGBColor with oversized components = %#x, want %#x", got, want)
	}
}

type mismatchError struct {
	got, want interface{}
}

func (e *mismatchError) Error() string {
	return "round-trip mismatch"
}
