package server

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestExistingConfigControlsRenderedAndInteractiveBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// A partial config predating the theme option and with an unknown key.
	config := `[appearance]
cursor_shape = "Bar"
cursor_blink = "On"
status_bar_clock = "Off"
unused_key = "legacy value"

[panes]
double_click_zoom = "Off"

[layout]
keyboard_resize_step = "4 cells"

[copy]
copy_key = "Ctrl-Insert"

[session]
confirm_shutdown = "Off"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QMM_CONFIG", path)
	s, c := newHelpServer(80, 24)
	peer := newHelpClient()
	s.clients[peer] = struct{}{}
	s.initConfig(filepath.Join(filepath.Dir(path), "server.log"))
	writePaneText(t, s, 1, "copy target")
	s.broadcast()
	for _, client := range []*clientConn{c, peer} {
		f := behaviorFrame(t, client)
		if f.CursorShape != protocol.CursorShapeBar || !f.CursorBlink {
			t.Errorf("loaded cursor style = %d, blink = %t, want blinking bar", f.CursorShape, f.CursorBlink)
		}
		if got := cellAt(f, f.Cols-4, f.Rows-1).Ch; got != ' ' {
			t.Errorf("clock-off status bar contains %q at the clock position", got)
		}
	}
	before := behaviorFrame(t, c).Splitters[0]
	s.handleClientMessage(clientMsg{c, protocol.ActionTogglePaneZoom{PaneID: 1}})
	if active := behaviorActivePane(t, c); active.Zoomed {
		t.Fatal("double-click zoom was enabled by a config that disables it")
	}
	sendBehaviorInput(s, c, "\x01\x1b[1;5C")
	for _, client := range []*clientConn{c, peer} {
		if got := behaviorFrame(t, client).Splitters[0].X; got != before.X+4 {
			t.Errorf("splitter after configured keyboard resize = %d, want %d", got, before.X+4)
		}
	}
	s.handleClientMessage(clientMsg{c, protocol.ActionSelectStart{X: 0, Y: 0}})
	s.handleClientMessage(clientMsg{c, protocol.ActionSelectUpdate{X: 3, Y: 0}})
	sendBehaviorInput(s, c, "\x1b[2;5~")
	if clip, ok := queuedClipboard(c); !ok || clip.Text != "copy" {
		t.Fatalf("clipboard after configured copy key = %#v, want copy", clip)
	}
	if !sendBehaviorInput(s, peer, "\x01&") {
		t.Error("disabled confirmation still requires a second key to shut down")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != config {
		t.Fatalf("using a valid existing config rewrote it: %q, %v", raw, err)
	}
}

func TestSavedLayoutV1RestoresThroughMenu(t *testing.T) {
	s, c := newLayoutTestServer(t)
	dir := filepath.Join(t.TempDir(), "pane-work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("QMM_CONFIG", configPath)
	s.initConfig(filepath.Join(filepath.Dir(configPath), "server.log"))
	path := filepath.Join(filepath.Dir(configPath), "layouts.toml")
	// Write the established file format directly to catch reader and writer
	// changes that a self-generated round trip would miss.
	fixture := fmt.Sprintf(`version = 1

[[layouts]]
name = "saved-lab"

[layouts.tree]
split = "vertical"
ratio = 0.375

[[layouts.tree.children]]
pane = 0

[[layouts.tree.children]]
split = "horizontal"
ratio = 0.625

[[layouts.tree.children.children]]
pane = 1

[[layouts.tree.children.children]]
pane = 2

[[layouts.panes]]
cwd = %q
command = ["/bin/sh", "-c", 'printf "%%s:%%s" "$1" "$(basename "$PWD")"; exec cat', "qmm-test", "left arg"]

[[layouts.panes]]
command = ["/bin/sh", "-c", "printf top-marker; exec cat"]

[[layouts.panes]]
command = ["/bin/sh", "-c", "printf bottom-marker; exec cat"]
`, dir)
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	s.initLayouts()
	writePaneText(t, s, 1, "original-window-marker")
	ready := make(chan struct{}, 1)
	s.sess.onOutput = func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	sendBehaviorInput(s, c, "\x01L")
	if text := frameText(behaviorFrame(t, c)); !strings.Contains(text, "saved-lab") || !strings.Contains(text, "3 panes") {
		t.Fatalf("v1 layout missing from the menu:\n%s", text)
	}
	sendBehaviorInput(s, c, "\r")
	f := behaviorFrame(t, c)
	wantPanes := []protocol.PaneRect{
		{Number: 0, X: 0, Y: 0, W: 30, H: 23, Active: true},
		{Number: 1, X: 31, Y: 0, W: 49, H: 14},
		{Number: 2, X: 31, Y: 15, W: 49, H: 8},
	}
	gotPanes := append([]protocol.PaneRect(nil), f.Panes...)
	for i := range gotPanes {
		gotPanes[i].ID = 0 // The allocator may change without changing visible behavior.
	}
	if !slices.Equal(gotPanes, wantPanes) {
		t.Fatalf("restored pane geometry = %+v, want %+v", gotPanes, wantPanes)
	}
	deadline := time.After(3 * time.Second)
	for {
		text := frameText(f)
		if strings.Contains(text, "left arg:pane-work") &&
			strings.Contains(text, "top-marker") &&
			strings.Contains(text, "bottom-marker") {
			break
		}
		select {
		case <-ready:
			s.broadcast()
			f = behaviorFrame(t, c)
		case <-deadline:
			t.Fatalf("restored commands did not render their output:\n%s", text)
		}
	}
	if len(f.Tabs) != 2 || f.Tabs[0].Name != "orig" || f.Tabs[1].Name != "saved-lab" || !f.Tabs[1].Active {
		t.Fatalf("tabs after restore = %+v", f.Tabs)
	}
	sendBehaviorInput(s, c, "\x01l")
	f = behaviorFrame(t, c)
	if !f.Tabs[0].Active || !strings.Contains(frameText(f), "original-window-marker") {
		t.Fatalf("returning from the restored layout did not recover the original window:\n%s", frameText(f))
	}
}
