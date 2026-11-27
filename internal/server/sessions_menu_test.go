package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/sessions"
)

func TestStatusBarShowsSessionBadge(t *testing.T) {
	sess := newSession()
	sess.name = "work"
	var comp compositor
	frame := comp.compose(sess, 80, 24, true)
	text := frameText(frame)
	if !strings.Contains(text, "[work]") {
		t.Fatalf("status bar missing [work] badge, got:\n%s", text)
	}
}

func TestSessionsMenuRenderAndInlineNaming(t *testing.T) {
	sess := newSession()
	sess.name = "default"
	list := []protocol.SessionInfo{
		{Name: "default", Windows: 2, Panes: 3, Clients: 1, Version: "v0.2.0"},
		{Name: "work", Windows: 1, Panes: 1, Clients: 0, Version: "v0.3.0"},
	}

	var comp compositor
	comp.sessionsOpen = true
	comp.sessionsList = list
	comp.sessionsCursor = 0
	frame := comp.compose(sess, 80, 24, true)
	text := frameText(frame)

	for _, want := range []string{"QMM SESSIONS", "CURRENT: default", "default", "[current]", "work", "+ New session"} {
		if !strings.Contains(text, want) {
			t.Errorf("sessions menu missing %q, got:\n%s", want, text)
		}
	}

	// Naming mode shows inline prompt and error when present.
	comp.sessionsNaming = true
	comp.sessionsCursor = len(list)
	comp.sessionsInput = "work"
	comp.sessionsError = `session "work" already exists`
	frame = comp.compose(sess, 80, 24, true)
	text = frameText(frame)
	if !strings.Contains(text, "New session: work") {
		t.Errorf("naming prompt missing, got:\n%s", text)
	}
	if !strings.Contains(text, `Error: session "work" already exists`) {
		t.Errorf("naming error missing, got:\n%s", text)
	}
}

func TestMultiSessionDiscoveryAndSwitchIntegration(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	t.Setenv("QMM_CONFIG", filepath.Join(dir, "config.toml"))

	errA := make(chan error, 1)
	go func() {
		errA <- Run(Config{SocketDir: dir, SessionName: "default", Version: "v1.0.0"})
	}()
	errB := make(chan error, 1)
	go func() {
		errB <- Run(Config{SocketDir: dir, SessionName: "work", Version: "v2.0.0"})
	}()

	sockA := filepath.Join(dir, "default")
	sockB := filepath.Join(dir, "work")
	waitSocket(t, sockA)
	waitSocket(t, sockB)

	// Ensure clean teardown of both servers at test end.
	defer func() {
		for _, sock := range []string{sockA, sockB} {
			if c, err := sessions.QueryInfo(sock, 200*time.Millisecond); err == nil && c.Name != "" {
				tc := dial(t, sock, 80, 24)
				_ = tc.codec.Send(protocol.Shutdown{})
				_ = tc.conn.Close()
			}
		}
	}()

	// Verify sessions.List discovers both live sessions with versions.
	list, err := sessions.List(dir)
	if err != nil {
		t.Fatalf("sessions.List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2 (%+v)", len(list), list)
	}
	byName := map[string]protocol.SessionInfo{}
	for _, info := range list {
		byName[info.Name] = info
	}
	if byName["default"].Version != "v1.0.0" || byName["work"].Version != "v2.0.0" {
		t.Fatalf("versions mismatch: %+v", byName)
	}

	// Attach to "default" and verify status bar shows [default].
	c1 := dial(t, sockA, 80, 24)
	defer c1.conn.Close()
	if _, ok := c1.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome")
	}
	f := c1.recvFrame()
	if !strings.Contains(frameText(f), "[default]") {
		t.Fatalf("status bar missing [default], got:\n%s", frameText(f))
	}

	// Press Ctrl-a S to open the sessions menu and wait until "work" appears.
	if err := c1.codec.Send(protocol.Input{Data: []byte{0x01, 'S'}}); err != nil {
		t.Fatalf("send Ctrl-a S: %v", err)
	}
	menuFrame := c1.waitForFrame(func(f protocol.Frame) bool {
		txt := frameText(f)
		return strings.Contains(txt, "QMM SESSIONS") && strings.Contains(txt, "work")
	})
	if !strings.Contains(frameText(menuFrame), "+ New session") {
		t.Fatalf("menu missing + New session:\n%s", frameText(menuFrame))
	}

	// Move down to "work" (depending on sort order, navigate until we select "work").
	// In our test, cursor starts on "default" (current). Determine if "work" is above or below.
	moveKey := []byte("j")
	if list[0].Name == "work" {
		moveKey = []byte("k")
	}
	if err := c1.codec.Send(protocol.Input{Data: moveKey}); err != nil {
		t.Fatalf("send move: %v", err)
	}
	if err := c1.codec.Send(protocol.Input{Data: []byte("\r")}); err != nil {
		t.Fatalf("send enter: %v", err)
	}

	// Expect SwitchSession{Name: "work", Create: false}.
	deadline := time.Now().Add(5 * time.Second)
	var switched *protocol.SwitchSession
	for time.Now().Before(deadline) {
		m := c1.recv()
		if sw, ok := m.(protocol.SwitchSession); ok {
			switched = &sw
			break
		}
	}
	if switched == nil {
		t.Fatal("did not receive SwitchSession")
	}
	if switched.Name != "work" || switched.Create {
		t.Fatalf("SwitchSession = %+v, want {Name:work Create:false}", *switched)
	}

	// Original session "default" must still be alive.
	if info, err := sessions.QueryInfo(sockA, time.Second); err != nil || info.Name != "default" {
		t.Fatalf("default session should still be alive: info=%+v err=%v", info, err)
	}

	// Re-attach to "default", open menu, press 'n', type "fresh" + Enter -> Create: true.
	c2 := dial(t, sockA, 80, 24)
	defer c2.conn.Close()
	if _, ok := c2.recv().(protocol.Welcome); !ok {
		t.Fatal("expected Welcome on c2")
	}
	_ = c2.recvFrame()
	_ = c2.codec.Send(protocol.Input{Data: []byte{0x01, 'S'}})
	c2.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "QMM SESSIONS")
	})
	_ = c2.codec.Send(protocol.Input{Data: []byte("n")})
	c2.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "New session:")
	})
	// Typing duplicate name "work" must show an error and stay open.
	_ = c2.codec.Send(protocol.Input{Data: []byte("work\r")})
	c2.waitForFrame(func(f protocol.Frame) bool {
		return strings.Contains(frameText(f), "already exists")
	})
	// Clear "work" with 4 backspaces, type "fresh" + Enter.
	_ = c2.codec.Send(protocol.Input{Data: []byte("\x7f\x7f\x7f\x7f")})
	_ = c2.codec.Send(protocol.Input{Data: []byte("fresh")})
	_ = c2.codec.Send(protocol.Input{Data: []byte("\r")})

	var created *protocol.SwitchSession
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := c2.recv()
		if sw, ok := m.(protocol.SwitchSession); ok {
			created = &sw
			break
		}
	}
	if created == nil {
		t.Fatal("did not receive SwitchSession for new session")
	}
	if created.Name != "fresh" || !created.Create {
		t.Fatalf("SwitchSession = %+v, want {Name:fresh Create:true}", *created)
	}

	// Cleanly shut down both servers.
	for _, sock := range []string{sockA, sockB} {
		tc := dial(t, sock, 80, 24)
		_ = tc.codec.Send(protocol.Shutdown{})
		_ = tc.conn.Close()
	}
	select {
	case err := <-errA:
		if err != nil {
			t.Fatalf("server A exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server A did not shut down")
	}
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("server B exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server B did not shut down")
	}
}
