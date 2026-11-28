package server

import (
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestPrefixAmpersandOpensStyledConfirmation(t *testing.T) {
	if !isExactBinding("&") {
		t.Fatal("prefix & is not registered")
	}
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "&")
	if !c.confirmOpen {
		t.Fatal("prefix & did not open the confirmation dialog")
	}
	if s.shutdownRequested {
		t.Fatal("opening the dialog requested shutdown")
	}
	if c.frame == nil {
		t.Fatal("confirmation binding did not render a frame")
	}
	f := *c.frame
	text := frameText(f)
	for _, want := range []string{
		"SHUTDOWN SESSION", confirmQuestion, confirmDetail,
		"y/Enter: shut down", "n/Esc: cancel",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible {
		t.Error("confirmation dialog should hide the pane cursor")
	}
	if len(f.Panes) != 0 || len(f.Splitters) != 0 || len(f.Tabs) != 0 {
		t.Error("confirmation dialog should hide pane and tab hit targets")
	}

	titleStyled, borderStyled := false, false
	for _, cell := range f.Cells {
		if cell.FG == colConfirmTitle && cell.Style&protocol.StyleBold != 0 && strings.ContainsRune(confirmTitle, cell.Ch) {
			titleStyled = true
		}
		if cell.FG == colConfirmBorder && (cell.Ch == '─' || cell.Ch == '╭') {
			borderStyled = true
		}
	}
	if !titleStyled || !borderStyled {
		t.Errorf("missing confirmation styling: title=%v border=%v", titleStyled, borderStyled)
	}
}

func TestConfirmationDialogIsPerClient(t *testing.T) {
	s, c1 := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}

	c1.confirmOpen = true
	c1.forceFull = true
	s.broadcast()

	if c1.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both client frames")
	}
	if !strings.Contains(frameText(*c1.frame), "SHUTDOWN SESSION") {
		t.Error("requesting client is missing the confirmation dialog")
	}
	if strings.Contains(frameText(*c2.frame), "SHUTDOWN SESSION") {
		t.Error("confirmation dialog leaked to another attached client")
	}
}

func TestConfirmationDialogConsumesInputAndActions(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.handleInput(c, []byte{0x01, '&'})
	if !c.confirmOpen {
		t.Fatal("prefix+& did not open the dialog through the input path")
	}

	// Ordinary typing must not reach a pane or end the session.
	s.handleInput(c, []byte("echo should-not-run\n"))
	if !c.confirmOpen || s.shutdownRequested {
		t.Fatal("typing changed the pending confirmation")
	}

	w := s.sess.activeWindow()
	w.activePane = 2
	if shutdown := s.handleClientMessage(clientMsg{cli: c, msg: protocol.ActionFocus{X: 0, Y: 0}}); shutdown {
		t.Fatal("mouse focus action requested shutdown")
	}
	if !c.confirmOpen || w.activePane != 2 {
		t.Error("mouse focus leaked through the confirmation dialog")
	}

	s.handleInput(c, []byte("n"))
	if c.confirmOpen || s.shutdownRequested {
		t.Fatal("n did not cancel the shutdown")
	}
	if c.frame == nil || strings.Contains(frameText(*c.frame), confirmQuestion) {
		t.Error("cancelled dialog is still rendered")
	}
}

func TestConfirmationAnswerKeys(t *testing.T) {
	for _, cancel := range []string{"n", "N", "q", "Q", "\x1b", "\x03"} {
		s, c := newHelpServer(80, 24)
		fireBinding(s, c, "&")
		s.handleInput(c, []byte(cancel))
		if c.confirmOpen || s.shutdownRequested {
			t.Errorf("cancel key %q = open:%v shutdown:%v", cancel, c.confirmOpen, s.shutdownRequested)
		}
	}
	for _, confirm := range []string{"y", "Y", "\r", "\n"} {
		s, c := newHelpServer(80, 24)
		fireBinding(s, c, "&")
		if !s.handleClientMessage(clientMsg{cli: c, msg: protocol.Input{Data: []byte(confirm)}}) {
			t.Errorf("confirm key %q did not request shutdown", confirm)
		}
		if !s.shutdownRequested {
			t.Errorf("confirm key %q did not set shutdown state", confirm)
		}
	}
}

func TestConfirmationDialogReplacesHelp(t *testing.T) {
	s, c := newHelpServer(80, 24)
	fireBinding(s, c, "?")
	if !c.helpOpen {
		t.Fatal("help did not open")
	}
	fireBinding(s, c, "&")
	if c.helpOpen || !c.confirmOpen {
		t.Fatalf("help=%v confirm=%v, want help:false confirm:true", c.helpOpen, c.confirmOpen)
	}
	if c.frame == nil || !strings.Contains(frameText(*c.frame), confirmQuestion) {
		t.Error("confirmation dialog was not rendered over help")
	}
}

func TestConfirmationDialogFitsSmallTerminal(t *testing.T) {
	comp := &clientView{confirmOpen: true}
	f := comp.compose(newTestSession(), 40, 10, true)
	text := frameText(f)
	for _, want := range []string{
		"SHUTDOWN SESSION", confirmQuestion, confirmDetail,
		"y/Enter: shut down", "n/Esc: cancel",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("small confirmation frame missing %q:\n%s", want, text)
		}
	}
	if f.CursorVisible || len(f.Panes) != 0 || len(f.Tabs) != 0 {
		t.Error("small confirmation frame did not hide session interaction")
	}
}
