package server

import (
	"strings"
	"testing"

	"github.com/demongiggle/cogito/internal/protocol"
)

func TestConfigWarningModalRendersAndBlocksInput(t *testing.T) {
	s, c := newHelpServer(80, 24)
	warning := configWarning{
		Reason:     "bad value",
		BackupPath: "/tmp/backup",
		ConfigPath: "/tmp/config",
	}
	s.showConfigWarning(c, warning)
	s.broadcast()
	if c.frame == nil {
		t.Fatal("warning modal did not render a frame")
	}
	f := *c.frame
	text := frameText(f)
	for _, want := range []string{
		"CONFIGURATION WARNING", "Reason: bad value", "Backup: /tmp/backup",
		"Config: /tmp/config", "q/Esc",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("warning frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible || len(f.Panes) != 0 || len(f.Splitters) != 0 || len(f.Tabs) != 0 {
		t.Error("warning modal should hide session interaction")
	}

	w := s.activeWindow()
	w.activePane = 2
	s.handleInput(c, []byte("echo should-not-run\n"))
	if !c.configWarningOpen {
		t.Fatal("unknown key dismissed the warning modal")
	}
	if s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: 0, Y: 0}}) {
		t.Fatal("mouse action requested shutdown through the warning modal")
	}
	if w.activePane != 2 {
		t.Error("mouse action leaked through the warning modal")
	}
}

func TestConfigWarningDismissKeys(t *testing.T) {
	for _, key := range []string{"q", "Q", "\x1b", "\x03", "\r", "\n", " "} {
		t.Run(keyName(key), func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			s.showConfigWarning(c, configWarning{Reason: "test", ConfigPath: "/tmp/config"})
			s.handleInput(c, []byte(key))
			if c.configWarningOpen {
				t.Fatalf("key %q did not dismiss the warning", key)
			}
			if c.frame == nil {
				t.Fatal("dismissing warning did not render a frame")
			}
			if strings.Contains(frameText(*c.frame), "CONFIGURATION WARNING") {
				t.Error("warning modal survived dismissal")
			}
		})
	}
}
