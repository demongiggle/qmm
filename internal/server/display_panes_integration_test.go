package server

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/cogito/internal/protocol"
)

// TestDisplayPaneNumbersViaPrefix drives prefix+q and a numeric jump through
// the real byte-stream state machine, then checks the focused pane in frame
// metadata.
func TestDisplayPaneNumbersViaPrefix(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	cfg := Config{SocketDir: dir}
	sock := filepath.Join(dir, "default")

	runErr := make(chan error, 1)
	go func() { runErr <- Run(cfg) }()
	waitSocket(t, sock)

	c := dial(t, sock, 80, 24)
	_ = c.recv() // Welcome
	_ = c.recvFrame()

	// Two panes: number 0 is the original, number 1 is the new one.
	if err := c.codec.Send(protocol.ActionSplit{Vertical: true}); err != nil {
		t.Fatalf("split: %v", err)
	}
	split := c.waitForFrame(func(f protocol.Frame) bool {
		return len(f.Panes) == 2 && len(f.Splitters) == 1
	})
	byNumber := map[int]int{}
	activeBefore := -1
	activeNumberBefore := -1
	for _, p := range split.Panes {
		byNumber[p.Number] = p.ID
		if p.Active {
			activeBefore = p.ID
			activeNumberBefore = p.Number
		}
	}
	if activeBefore == -1 || activeNumberBefore == -1 || len(byNumber) != 2 {
		t.Fatalf("split frame panes = %+v", split.Panes)
	}
	targetNumber := 0
	if byNumber[0] == activeBefore {
		targetNumber = 1
	}
	targetID := byNumber[targetNumber]

	if err := c.codec.Send(protocol.Input{Data: []byte("\x01q")}); err != nil {
		t.Fatalf("prefix q: %v", err)
	}
	overlay := c.waitForFrame(func(f protocol.Frame) bool {
		text := frameText(f)
		return f.Full && strings.Contains(text, "panes 0-1") && strings.Contains(text, "000")
	})
	if activePaneNumber(overlay) != activeNumberBefore {
		t.Fatalf("overlay active pane number = %d, want %d", activePaneNumber(overlay), activeNumberBefore)
	}

	if err := c.codec.Send(protocol.Input{Data: []byte(strconv.Itoa(targetNumber))}); err != nil {
		t.Fatalf("select pane %d: %v", targetNumber, err)
	}
	c.waitForFrame(func(f protocol.Frame) bool {
		if !f.Full || strings.Contains(frameText(f), "panes 0-1") {
			return false
		}
		for _, p := range f.Panes {
			if p.Active && p.ID == targetID {
				return true
			}
		}
		return false
	})

	if err := c.codec.Send(protocol.Shutdown{}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
