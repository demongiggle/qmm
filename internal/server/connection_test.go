package server

import (
	"errors"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// pipeWriteProbe reports when the server reaches its next write. net.Pipe
// keeps that write blocked until the test client reads or closes its end.
type pipeWriteProbe struct {
	net.Conn
	nextWrite chan chan struct{}
}

func (p *pipeWriteProbe) Write(b []byte) (int, error) {
	select {
	case started := <-p.nextWrite:
		close(started)
	default:
	}
	return p.Conn.Write(b)
}

func newConnectionTestServer(queueSize int) *Server {
	return &Server{
		sess:      newTestSession(),
		cols:      80,
		rows:      24,
		clients:   make(map[*clientConn]struct{}),
		msgs:      make(chan clientMsg, queueSize),
		deadConns: make(chan *clientConn, 4),
	}
}

func attachPipeClient(t *testing.T, s *Server, cols, rows int) (*clientConn, *testClient, *pipeWriteProbe) {
	t.Helper()
	server, peer := net.Pipe()
	probe := &pipeWriteProbe{Conn: server, nextWrite: make(chan chan struct{}, 1)}
	c := newHelpClient()
	c.conn, c.codec = probe, protocol.NewCodec(probe)
	c.cols, c.rows = cols, rows
	client := &testClient{t: t, conn: peer, codec: protocol.NewCodec(peer)}
	registered := make(chan struct{})
	t.Cleanup(func() {
		_ = peer.Close()
		_ = server.Close()
		waitConnectionEvent(t, registered)
		s.removeClient(c)
	})
	go func() {
		s.registerClient(c)
		close(registered)
	}()
	// Bound a stalled test even if the server starts blocking on an outgoing
	// frame while the peer is paused.
	deadline := time.Now().Add(5 * time.Second)
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	msg := client.recv()
	welcome, ok := msg.(protocol.Welcome)
	if !ok {
		t.Fatalf("first message = %#v, want Welcome", msg)
	}
	waitConnectionEvent(t, registered)
	if f := client.recvFrame(); !f.Full || f.Cols != welcome.Cols || f.Rows != welcome.Rows {
		t.Fatalf("first frame = %dx%d, full=%t; want a full frame matching Welcome %+v", f.Cols, f.Rows, f.Full, welcome)
	}
	return c, client, probe
}

func waitConnectionEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for connection event")
		var zero T
		return zero
	}
}

func sendPipeClientMessage(t *testing.T, s *Server, c *clientConn, client *testClient, msg any) {
	t.Helper()
	if err := client.codec.Send(msg); err != nil {
		t.Fatal(err)
	}
	in := waitConnectionEvent(t, s.msgs)
	if in.cli != c {
		t.Fatal("server attributed the input to the wrong client")
	}
	if s.handleClientMessage(in) {
		t.Fatal("client input requested an unexpected shutdown")
	}
}

func TestConnectionSlowClientKeepsDeltasAndDoesNotBlockPeer(t *testing.T) {
	s := newConnectionTestServer(8)
	fast, fastPeer, _ := attachPipeClient(t, s, 80, 24)
	slow, slowPeer, probe := attachPipeClient(t, s, 80, 24)
	_ = fastPeer.recvFrame() // attaching slow broadcasts a new full frame

	started := make(chan struct{})
	probe.nextWrite <- started
	notice := protocol.Notice{Text: "pause the writer"}
	slow.enqueue(notice)
	waitConnectionEvent(t, started)

	writePaneText(t, s, 1, "AX")
	s.broadcast()
	first := fastPeer.recvFrame()
	if first.Full || !hasCell(first, 0, 0, 'A') || !hasCell(first, 1, 0, 'X') {
		t.Fatal("the fast client did not receive the first incremental update")
	}

	writePaneText(t, s, 1, "\x1b[1;2HY")
	writePaneText(t, s, 2, "\x1b[3;4HB")
	sendPipeClientMessage(t, s, fast, fastPeer, protocol.ActionFocus{X: 42, Y: 2})
	latest := fastPeer.recvFrame()
	if latest.Full || !hasCell(latest, 1, 0, 'Y') || !hasCell(latest, 44, 2, 'B') {
		t.Fatal("the fast client did not receive the second incremental update during the stall")
	}
	if !latest.CursorVisible || latest.CursorX != 45 || latest.CursorY != 2 || len(latest.Panes) != 2 || !latest.Panes[1].Active {
		t.Fatalf("the fast client's focus/cursor update did not take effect: panes=%+v cursor=(%d,%d)", latest.Panes, latest.CursorX, latest.CursorY)
	}

	clipboard := protocol.Clipboard{Text: "queued control message"}
	slow.enqueue(clipboard)
	if got := slowPeer.recv(); got != notice {
		t.Fatalf("first message after resuming = %#v, want notice", got)
	}
	if got := slowPeer.recv(); got != clipboard {
		t.Fatalf("message after notice = %#v, want clipboard before the pending frame", got)
	}
	merged := slowPeer.recvFrame()
	if !hasCell(merged, 0, 0, 'A') || !hasCell(merged, 1, 0, 'Y') || !hasCell(merged, 44, 2, 'B') {
		t.Errorf("coalesced frame lost an old cell or failed to replace it: %+v", merged.Cells)
	}
	if merged.CursorVisible != latest.CursorVisible || merged.CursorX != latest.CursorX || merged.CursorY != latest.CursorY ||
		!slices.Equal(merged.Panes, latest.Panes) || !slices.Equal(merged.Splitters, latest.Splitters) {
		t.Error("coalesced frame lost the latest cursor or pane geometry")
	}
}

func TestConnectionReaderAndWriterDisconnectRemoveClientOnce(t *testing.T) {
	s := newConnectionTestServer(8)
	steady, steadyPeer, _ := attachPipeClient(t, s, 100, 30)
	small, smallPeer, probe := attachPipeClient(t, s, 80, 24)
	if f := steadyPeer.recvFrame(); f.Cols != 80 || f.Rows != 24 {
		t.Fatalf("shared frame = %dx%d, want 80x24", f.Cols, f.Rows)
	}
	writePaneText(t, s, 1, "session survives")
	started := make(chan struct{})
	probe.nextWrite <- started
	small.enqueue(protocol.Notice{Text: "pending during disconnect"})
	waitConnectionEvent(t, started)
	if err := smallPeer.conn.Close(); err != nil {
		t.Fatal(err)
	}

	// Closing the peer breaks both the blocking write and the concurrent
	// read. Apply both notifications just as Run's event loop does.
	for range 2 {
		got := waitConnectionEvent(t, s.deadConns)
		if got != small {
			t.Fatal("a disconnect was attributed to the surviving client")
		}
		s.removeClient(got)
	}
	if len(s.clients) != 1 {
		t.Fatalf("attached clients = %d, want just the survivor", len(s.clients))
	}
	if f := steadyPeer.recvFrame(); !f.Full || f.Cols != 100 || f.Rows != 30 || !hasCell(f, 0, 0, 's') {
		t.Fatalf("survivor did not receive its resized live pane: full=%t size=%dx%d", f.Full, f.Cols, f.Rows)
	}
	sendPipeClientMessage(t, s, steady, steadyPeer, protocol.ActionFocus{X: 60, Y: 2})
	if f := steadyPeer.recvFrame(); len(f.Panes) != 2 || !f.Panes[1].Active {
		t.Fatal("surviving client could not focus the other pane")
	}
}

func TestConnectionDropsClientWhenInputQueueIsFull(t *testing.T) {
	s := newConnectionTestServer(1)
	c, peer, _ := attachPipeClient(t, s, 80, 24)
	first := protocol.Input{Data: []byte("keep")}
	if err := peer.codec.Send(first); err != nil {
		t.Fatal(err)
	}
	if err := peer.codec.Send(protocol.Input{Data: []byte("overflow")}); err != nil {
		t.Fatal(err)
	}
	if got := waitConnectionEvent(t, s.deadConns); got != c {
		t.Fatal("the overflowing client did not report disconnection")
	}
	in := waitConnectionEvent(t, s.msgs)
	input, ok := in.msg.(protocol.Input)
	if in.cli != c || !ok || !slices.Equal(input.Data, first.Data) {
		t.Fatalf("message in the full queue = %#v, want the original input", in.msg)
	}
	s.removeClient(c)
	if _, err := peer.codec.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("reading the dropped connection returned %v, want EOF", err)
	}
}
