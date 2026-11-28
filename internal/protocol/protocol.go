// Package protocol defines the wire format spoken between qmm clients and
// the qmm session server over the Unix domain socket. Messages are Go
// values encoded with encoding/gob.
package protocol

import (
	"encoding/gob"
	"fmt"
	"net"
	"sync"
)

// Direction for pane-resize actions.
const (
	DirUp uint8 = iota
	DirDown
	DirLeft
	DirRight
)

// Native cursor shapes. Zero is the steady block default for frames created
// by older callers or tests that omit the cursor style fields.
const (
	CursorShapeBlock uint8 = iota
	CursorShapeUnderline
	CursorShapeBar
)

// Cell style bit flags.
const (
	StyleBold uint8 = 1 << iota
	StyleItalic
	StyleUnderline
	StyleBlink
	StyleReverse
)

// Cell color encodings. Palette indexes 0-255 refer to the xterm palette;
// ColorRGBFlag marks a 24-bit RGB value packed as 0xRRGGBB.
const (
	ColorDefault int32 = -1
	ColorRGBFlag int32 = 1 << 24
)

// RGBColor encodes a 24-bit RGB color for a Cell foreground or background.
func RGBColor(r, g, b int32) int32 {
	return ColorRGBFlag | (r&0xff)<<16 | (g&0xff)<<8 | (b & 0xff)
}

// ---------------------------------------------------------------------------
// client -> server messages
// ---------------------------------------------------------------------------

// Hello is the first message sent by a client after connecting.
type Hello struct {
	Term string
	Cols int
	Rows int
}

// Input carries raw bytes typed by the user destined for the active pane.
// The server runs its own prefix-key state machine over this stream.
type Input struct {
	Data []byte
}

// Resize is sent when the client terminal changes size.
type Resize struct {
	Cols int
	Rows int
}

// Detach asks the server to detach this client (equivalent of prefix+d).
type Detach struct{}

// Shutdown asks the server to terminate the whole session (qmm kill-server).
type Shutdown struct{}

// InfoRequest asks a running session server for its SessionInfo without
// attaching as a client. Like Shutdown, it is accepted before Hello.
type InfoRequest struct{}

// ActionFocus requests keyboard focus on the pane containing cell (X, Y).
type ActionFocus struct {
	X, Y int
}

// ActionSelectWindow requests switching to window Index.
type ActionSelectWindow struct {
	Index int
}

// ActionNewWindow opens a new window.
type ActionNewWindow struct{}

// ActionSplit splits the active pane (Vertical: divider is a vertical line).
type ActionSplit struct {
	Vertical bool
}

// ActionKillPane kills the active pane.
type ActionKillPane struct{}

// ActionNextWindow / ActionPrevWindow switch windows.
type ActionNextWindow struct{}
type ActionPrevWindow struct{}

// ActionCyclePane focuses the next pane in tree order.
type ActionCyclePane struct{}

// ActionToggleZoom toggles zoom on the active pane.
type ActionToggleZoom struct{}

// ActionTogglePaneZoom toggles zoom for a pane selected by a mouse
// double-click. The pane is focused before it becomes the floating overlay.
type ActionTogglePaneZoom struct {
	PaneID int
}

// ActionResizePane grows/shrinks the active pane in Direction by Delta cells.
type ActionResizePane struct {
	Direction uint8
	Delta     int
}

// ActionDragStart begins a border drag on the split identified by Splitter.
type ActionDragStart struct {
	Splitter int
}

// ActionDragUpdate moves a splitter by Delta cells since the previous event.
// Positive deltas move the divider down/right.
type ActionDragUpdate struct {
	Splitter int
	Delta    int
}

// ActionDragEnd finishes a border drag.
type ActionDragEnd struct {
	Splitter int
}

// ActionWheel reports a mouse wheel step. Delta is negative for wheel-up
// (towards older scrollback) and positive for wheel-down. The server uses it
// to scroll copy mode, or to enter copy mode on the first wheel-up.
type ActionWheel struct {
	Delta int
}

// ActionSelectStart begins a mouse text selection anchored at cell (X, Y) in
// screen coordinates. Sent when a drag crosses into a different cell than the
// one the press started in. The server snapshots the pane under the pointer
// into a non-modal selection view and starts a character-wise selection there;
// no copy mode is opened and no other input is captured.
type ActionSelectStart struct {
	X, Y int
}

// ActionSelectUpdate extends an in-progress mouse selection to cell (X, Y) in
// screen coordinates. Sent for every pointer motion event while a selection
// drag is active.
type ActionSelectUpdate struct {
	X, Y int
}

// ---------------------------------------------------------------------------
// server -> client messages
// ---------------------------------------------------------------------------

// Welcome is sent in reply to Hello.
type Welcome struct {
	Cols int // effective (minimum) session size
	Rows int
}

// Cell describes one terminal cell. FG/BG are ColorDefault for the terminal
// default, an xterm palette index 0-255, or ColorRGBFlag plus a 24-bit RGB
// value (as produced by RGBColor).
type Cell struct {
	Row, Col int
	Ch       rune
	FG, BG   int32
	Style    uint8
}

// PaneRect describes where a pane is rendered on screen (for client hit-testing).
// Number is the pane's zero-based index within its window, as shown by
// prefix+q. Zoomed marks the floating overlay in a zoomed window; the
// underlying split panes are also reported so they remain visible
// double-click targets.
type PaneRect struct {
	ID         int
	Number     int
	X, Y, W, H int
	Active     bool
	Zoomed     bool
}

// Splitter describes an internal divider between panes (for drag-resize).
type Splitter struct {
	ID       int
	X, Y     int
	Len      int
	Vertical bool
}

// Tab describes one entry in the status-bar window list.
type Tab struct {
	Index      int
	Start, End int // inclusive-exclusive column range on the status row
	Name       string
	Active     bool
	Zoomed     bool
}

// Frame carries screen updates. When Full is true, Cells covers the whole
// screen and the client should clear before drawing.
type Frame struct {
	Full          bool
	Cols, Rows    int
	Cells         []Cell
	CursorX       int
	CursorY       int
	CursorVisible bool
	CursorShape   uint8
	CursorBlink   bool
	Panes         []PaneRect
	Splitters     []Splitter
	Tabs          []Tab
}

// Notice is a transient message for the client to surface (e.g. ring the bell).
type Notice struct {
	Text string
}

// Clipboard asks the client to put Text in the host terminal's clipboard via
// OSC 52. It is sent only to the client that made the copy selection.
type Clipboard struct {
	Text string
}

// Goodbye is sent before the server closes the connection.
type Goodbye struct {
	Reason string
}

// SessionInfo describes a running session daemon in reply to InfoRequest.
type SessionInfo struct {
	Name      string
	Windows   int
	Panes     int
	Clients   int
	CreatedAt int64
	Version   string
	PID       int
}

// SwitchSession instructs an attached client to disconnect from the current
// session and switch to Name (spawning a fresh session first when Create is
// true).
type SwitchSession struct {
	Name   string
	Create bool
}

func init() {
	gob.Register(Hello{})
	gob.Register(Input{})
	gob.Register(Resize{})
	gob.Register(Detach{})
	gob.Register(Shutdown{})
	gob.Register(InfoRequest{})
	gob.Register(ActionFocus{})
	gob.Register(ActionSelectWindow{})
	gob.Register(ActionNewWindow{})
	gob.Register(ActionSplit{})
	gob.Register(ActionKillPane{})
	gob.Register(ActionNextWindow{})
	gob.Register(ActionPrevWindow{})
	gob.Register(ActionCyclePane{})
	gob.Register(ActionToggleZoom{})
	gob.Register(ActionTogglePaneZoom{})
	gob.Register(ActionResizePane{})
	gob.Register(ActionDragStart{})
	gob.Register(ActionDragUpdate{})
	gob.Register(ActionDragEnd{})
	gob.Register(ActionWheel{})
	gob.Register(ActionSelectStart{})
	gob.Register(ActionSelectUpdate{})

	gob.Register(Welcome{})
	gob.Register(Frame{})
	gob.Register(Notice{})
	gob.Register(Clipboard{})
	gob.Register(Goodbye{})
	gob.Register(SessionInfo{})
	gob.Register(SwitchSession{})
}

// Codec wraps a net.Conn with gob encoding. Reads are unsynchronised (one
// goroutine); writes are serialised with a mutex.
type Codec struct {
	conn net.Conn
	enc  *gob.Encoder
	dec  *gob.Decoder
	wmu  sync.Mutex
}

// NewCodec returns a Codec wrapping conn.
func NewCodec(conn net.Conn) *Codec {
	return &Codec{conn: conn, enc: gob.NewEncoder(conn), dec: gob.NewDecoder(conn)}
}

// Send encodes and writes a single message.
func (c *Codec) Send(msg interface{}) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	// Encode the interface pointer so gob transmits the concrete type's
	// registered name; decoding happens into an interface value too.
	return c.enc.Encode(&msg)
}

// Recv reads the next message. Callers may type-assert the result to any of
// the concrete message types declared in this package.
func (c *Codec) Recv() (interface{}, error) {
	var msg interface{}
	if err := c.dec.Decode(&msg); err != nil {
		return nil, fmt.Errorf("protocol: decode: %w", err)
	}
	return derefMessage(msg), nil
}

func derefMessage(msg interface{}) interface{} {
	switch m := msg.(type) {
	case *Hello:
		return *m
	case *Input:
		return *m
	case *Resize:
		return *m
	case *Detach:
		return *m
	case *Shutdown:
		return *m
	case *InfoRequest:
		return *m
	case *ActionFocus:
		return *m
	case *ActionSelectWindow:
		return *m
	case *ActionNewWindow:
		return *m
	case *ActionSplit:
		return *m
	case *ActionKillPane:
		return *m
	case *ActionNextWindow:
		return *m
	case *ActionPrevWindow:
		return *m
	case *ActionCyclePane:
		return *m
	case *ActionToggleZoom:
		return *m
	case *ActionTogglePaneZoom:
		return *m
	case *ActionResizePane:
		return *m
	case *ActionDragStart:
		return *m
	case *ActionDragUpdate:
		return *m
	case *ActionDragEnd:
		return *m
	case *ActionWheel:
		return *m
	case *Welcome:
		return *m
	case *Frame:
		return *m
	case *Notice:
		return *m
	case *Clipboard:
		return *m
	case *Goodbye:
		return *m
	case *SessionInfo:
		return *m
	case *SwitchSession:
		return *m
	default:
		return msg
	}
}
