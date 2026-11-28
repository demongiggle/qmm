package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func sendBehaviorInput(s *Server, c *clientConn, keys ...string) bool {
	for _, key := range keys {
		if s.handleClientMessage(clientMsg{cli: c, msg: protocol.Input{Data: []byte(key)}}) {
			return true
		}
	}
	return false
}

func behaviorFrame(t *testing.T, c *clientConn) protocol.Frame {
	t.Helper()
	if c.frame == nil || !c.frame.Full {
		t.Fatal("expected a complete pending frame")
	}
	return *c.frame
}

func behaviorActivePane(t *testing.T, c *clientConn) protocol.PaneRect {
	t.Helper()
	for _, pane := range behaviorFrame(t, c).Panes {
		if pane.Active {
			return pane
		}
	}
	t.Fatal("frame has no active pane")
	return protocol.PaneRect{}
}

func capturePaneInput(t *testing.T, p *pane) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pane-input")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	p.ptmx = f
	return f
}

func requirePaneInput(t *testing.T, f *os.File, want string) {
	t.Helper()
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("pane input = %q, want %q", got, want)
	}
}

func TestOverlayRoutesMouseAndKeyboardPerClient(t *testing.T) {
	cases := []struct {
		name, prefix, marker string
		mouseDismisses       bool
	}{
		{"help", "?", "QMM HELP", true},
		{"settings", "s", "QMM SETTINGS", true},
		{"sessions", "S", "QMM SESSIONS", true},
		{"saved layouts", "L", "QMM LAYOUTS", true},
		{"pane numbers", "q", "number selects", true},
		{"shutdown confirmation", "&", "SHUTDOWN SESSION", false},
		{"config recovery", "", "CONFIGURATION WARNING", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, event := range []struct {
				name    string
				message any
			}{
				{"focus", protocol.ActionFocus{X: 42, Y: 2}},
				{"wheel", protocol.ActionWheel{Delta: -1}},
				{"Escape", protocol.Input{Data: []byte("\x1b")}},
			} {
				t.Run(event.name, func(t *testing.T) {
					s, owner := newHelpServer(80, 24)
					peer := newHelpClient()
					s.clients[peer] = struct{}{}
					s.layoutsPath = filepath.Join(t.TempDir(), "layouts.toml")
					input := capturePaneInput(t, s.sess.activePane())
					writePaneText(t, s, 1, "live-pane-marker")
					if tc.prefix == "" {
						s.showConfigWarning(owner, configWarning{Reason: "invalid TOML", ConfigPath: "config.toml"})
						s.broadcast()
					} else {
						sendBehaviorInput(s, owner, "\x01"+tc.prefix)
					}
					ownerShowsOverlay := func() bool {
						return strings.Contains(frameText(behaviorFrame(t, owner)), tc.marker)
					}
					if !ownerShowsOverlay() {
						t.Fatalf("owner frame is missing %q:\n%s", tc.marker, frameText(behaviorFrame(t, owner)))
					}
					peerText := frameText(behaviorFrame(t, peer))
					if strings.Contains(peerText, tc.marker) || !strings.Contains(peerText, "live-pane-marker") {
						t.Fatal("opening an overlay changed the peer's live view")
					}
					sendBehaviorInput(s, peer, "peer-live|")
					if s.handleClientMessage(clientMsg{cli: owner, msg: event.message}) {
						t.Fatal("dismissal event requested shutdown")
					}
					wantOverlay := event.name != "Escape" && !tc.mouseDismisses
					if got := ownerShowsOverlay(); got != wantOverlay {
						t.Fatalf("overlay visible after %s = %t, want %t", event.name, got, wantOverlay)
					}
					// A dismissal consumes the triggering event. It must neither
					// move shared focus nor enter copy mode under the overlay.
					if got := behaviorActivePane(t, peer).Number; got != 0 {
						t.Fatalf("peer's active pane changed to %d during dismissal", got)
					}
					if wantOverlay {
						sendBehaviorInput(s, owner, "\x1b")
					}
					if text := frameText(behaviorFrame(t, owner)); !strings.Contains(text, "live-pane-marker") || strings.Contains(text, "[copy mode]") {
						t.Fatalf("dismissal did not restore the live pane:\n%s", text)
					}
					sendBehaviorInput(s, owner, "owner-live")
					requirePaneInput(t, input, "peer-live|owner-live")
					s.handleClientMessage(clientMsg{cli: owner, msg: protocol.ActionFocus{X: 42, Y: 2}})
					for _, c := range []*clientConn{owner, peer} {
						if got := behaviorActivePane(t, c).Number; got != 1 {
							t.Errorf("active pane after normal click = %d, want 1", got)
						}
					}
				})
			}
		})
	}
}

func TestPrefixInputConsumesOnlyTheCommandBytes(t *testing.T) {
	s, c := newHelpServer(80, 24)
	input := capturePaneInput(t, s.sess.activePane())
	sendBehaviorInput(s, c, "before", "\x01", "\x01after")
	requirePaneInput(t, input, "before\x01after")

	sendBehaviorInput(s, c, "\x01~tail")
	requirePaneInput(t, input, "before\x01aftertail")
	if len(c.queue) != 1 || c.queue[0] != (protocol.Notice{Text: "qmm: unbound prefix key"}) {
		t.Fatalf("unbound prefix messages = %#v", c.queue)
	}

	sendBehaviorInput(s, c, "\x01")
	// Simulate elapsed wall time so the expiration test needs no sleep.
	c.prefixAt = time.Now().Add(-time.Hour)
	sendBehaviorInput(s, c, "?")
	requirePaneInput(t, input, "before\x01aftertail?")
}

func TestSessionMenuRefreshKeepsSelectionAndIgnoresLateResults(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.name = "current"
	s.sess.name = "current"
	sendBehaviorInput(s, c, "\x01S")
	refresh := func(names ...string) {
		var list []protocol.SessionInfo
		for _, name := range names {
			list = append(list, protocol.SessionInfo{Name: name, Windows: 9})
		}
		s.handleClientMessage(clientMsg{cli: c, msg: sessionListResult{list: list}})
	}
	refresh("current", "ops", "dev")
	sendBehaviorInput(s, c, "j", "r")
	refresh("dev", "current", "ops")
	text := frameText(behaviorFrame(t, c))
	var currentRow string
	for _, row := range strings.Split(text, "\n") {
		if strings.Contains(row, "│") && strings.Contains(row, "[current]") {
			currentRow = row
			break
		}
	}
	if !strings.Contains(text, "▸ ops") || !strings.Contains(currentRow, "1 win · 2 panes · 1 att") {
		t.Fatalf("refresh did not preserve selection and current-session metadata:\n%s", text)
	}

	sendBehaviorInput(s, c, "n", "dev", "\r")
	text = frameText(behaviorFrame(t, c))
	if !strings.Contains(text, "already exists") || !strings.Contains(text, "New session: dev") {
		t.Fatalf("duplicate name did not keep the naming prompt:\n%s", text)
	}
	sendBehaviorInput(s, c, "\x1b", "q")
	refresh("late")
	text = frameText(behaviorFrame(t, c))
	if strings.Contains(text, "QMM SESSIONS") || strings.Contains(text, "late") {
		t.Fatalf("late discovery result reopened the menu:\n%s", text)
	}
}

func TestConfigWarningDuringCopyKeepsMouseBlockedUntilBothViewsClose(t *testing.T) {
	s, c := newHelpServer(80, 24)
	input := capturePaneInput(t, s.sess.activePane())
	sendBehaviorInput(s, c, "\x01[")
	s.showConfigWarning(c, configWarning{Reason: "test warning"})
	s.broadcast()
	// Copy mode gets keys first, but a warning blocks the outside click that
	// would normally close copy mode and focus the second pane.
	s.handleClientMessage(clientMsg{c, protocol.ActionFocus{X: 42, Y: 2}})
	if !strings.Contains(frameText(behaviorFrame(t, c)), "[copy mode]") {
		t.Fatal("outside click dismissed copy mode under a pending warning")
	}
	sendBehaviorInput(s, c, "q")
	if !strings.Contains(frameText(behaviorFrame(t, c)), "CONFIGURATION WARNING") {
		t.Fatal("leaving copy mode did not reveal the pending warning")
	}
	sendBehaviorInput(s, c, "\x1b", "live")
	requirePaneInput(t, input, "live")
	if got := behaviorActivePane(t, c).Number; got != 0 {
		t.Fatalf("blocked click moved focus to pane %d", got)
	}
}

func TestBatchedPrefixChordsPreserveMenuLifetimes(t *testing.T) {
	for _, tc := range []struct {
		name, chord, top string
		revealsSessions  bool
	}{
		{"copy over sessions", "[", "[copy mode]", true},
		{"settings replaces sessions", "s", "QMM SETTINGS", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			input := capturePaneInput(t, s.sess.activePane())
			// Both prefix chords arrive in one protocol.Input message.
			sendBehaviorInput(s, c, "\x01S\x01"+tc.chord)
			if !strings.Contains(frameText(behaviorFrame(t, c)), tc.top) {
				t.Fatalf("missing top view %q", tc.top)
			}
			sendBehaviorInput(s, c, "q")
			if got := strings.Contains(frameText(behaviorFrame(t, c)), "QMM SESSIONS"); got != tc.revealsSessions {
				t.Fatalf("sessions visible after closing %s = %t", tc.top, got)
			}
			if tc.revealsSessions {
				sendBehaviorInput(s, c, "q")
			}
			sendBehaviorInput(s, c, "live")
			requirePaneInput(t, input, "live")
		})
	}
}
