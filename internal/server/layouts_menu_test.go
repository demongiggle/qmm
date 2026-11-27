package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/layout"
)

func layoutsMenuFixture() []savedLayout {
	tr := layout.New(1)
	tr.Split(1, 2, layout.Vertical)
	return []savedLayout{
		{Name: "dev", Tree: tr.Spec(), Panes: []savedPane{{}, {Command: []string{"nvim", "main.go"}}}},
		{Name: "ops", Tree: layout.New(1).Spec(), Panes: []savedPane{{}}},
	}
}

func TestLayoutsMenuRender(t *testing.T) {
	sess := newTestSession()
	var comp compositor
	comp.layoutsOpen = true
	comp.layoutsList = layoutsMenuFixture()
	comp.layoutsCursor = 0
	frame := comp.compose(sess, 80, 24, true)
	text := frameText(frame)
	for _, want := range []string{
		"QMM LAYOUTS", "PREFIX: Ctrl-a", "SAVED LAYOUTS", "dev", "2 panes",
		"ops", "1 pane", "+ Save current window", "Enter restore", "s save", "d delete",
	} {
		if !strings.Contains(strings.ToUpper(text), strings.ToUpper(want)) && !strings.Contains(text, want) {
			t.Errorf("layouts menu missing %q, got:\n%s", want, text)
		}
	}
	if frame.CursorVisible {
		t.Error("layouts menu should hide the pane cursor")
	}
	if len(frame.Panes) != 0 || len(frame.Splitters) != 0 {
		t.Error("layouts menu should hide pane hit targets")
	}

	comp.layoutsNaming = true
	comp.layoutsCursor = len(comp.layoutsList)
	comp.layoutsInput = "dev"
	frame = comp.compose(sess, 80, 24, true)
	text = frameText(frame)
	if !strings.Contains(text, "Save layout as: dev") || !strings.Contains(text, "Enter save") {
		t.Fatalf("naming prompt missing, got:\n%s", text)
	}

	comp.layoutsNaming = false
	comp.layoutsConfirmOverwrite = "dev"
	comp.layoutsMessage = `Layout "dev" exists — overwrite? y/n`
	frame = comp.compose(sess, 80, 24, true)
	text = frameText(frame)
	if !strings.Contains(text, "overwrite? y/n") || !strings.Contains(text, "y overwrite") {
		t.Fatalf("overwrite prompt missing, got:\n%s", text)
	}
}

func TestLayoutsMenuOpensFromPrefixAndIsPerClient(t *testing.T) {
	s, c1 := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}
	s.layoutsPath = filepath.Join(t.TempDir(), "layouts.toml")

	if !isExactBinding("L") {
		t.Fatal("prefix L is not registered")
	}
	fireBinding(s, c1, "L")
	if !c1.layoutsOpen {
		t.Fatal("prefix L did not open the layout menu")
	}
	if c1.frame == nil || c2.frame == nil {
		t.Fatal("broadcast did not render both clients")
	}
	if !strings.Contains(frameText(*c1.frame), "QMM LAYOUTS") {
		t.Errorf("requesting client is missing the layouts menu:\n%s", frameText(*c1.frame))
	}
	if strings.Contains(frameText(*c2.frame), "QMM LAYOUTS") {
		t.Errorf("layouts menu leaked to another client:\n%s", frameText(*c2.frame))
	}
}

func TestLayoutsMenuSaveOverwriteAndDelete(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.layoutsPath = filepath.Join(t.TempDir(), "layouts.toml")
	fireBinding(s, c, "L")

	s.handleLayoutsMenuInput(c, []byte("s"))
	if !c.layoutsNaming {
		t.Fatal("s did not start naming")
	}
	s.handleLayoutsMenuInput(c, []byte("dev"))
	if c.layoutsInput != "dev" {
		t.Fatalf("input = %q, want dev", c.layoutsInput)
	}
	s.handleLayoutsMenuInput(c, []byte("\r"))
	if c.layoutsNaming || len(c.layoutsList) != 1 {
		t.Fatalf("after save: naming=%v list=%d", c.layoutsNaming, len(c.layoutsList))
	}
	if !strings.Contains(c.layoutsMessage, "Saved") {
		t.Fatalf("save message = %q", c.layoutsMessage)
	}
	if list, warning, _ := loadLayouts(s.layoutsPath); warning != nil || len(list) != 1 || list[0].Name != "dev" {
		t.Fatalf("saved file = %+v warning=%+v", list, warning)
	}

	// Saving the same name prompts before replacing it.
	s.handleLayoutsMenuInput(c, []byte("s"))
	s.handleLayoutsMenuInput(c, []byte("dev\r"))
	if c.layoutsConfirmOverwrite != "dev" {
		t.Fatalf("overwrite confirmation = %q, want dev", c.layoutsConfirmOverwrite)
	}
	if !strings.Contains(c.layoutsMessage, "overwrite? y/n") {
		t.Fatalf("overwrite message = %q", c.layoutsMessage)
	}
	s.handleLayoutsMenuInput(c, []byte("n"))
	if !c.layoutsNaming || c.layoutsInput != "dev" {
		t.Fatalf("cancel overwrite = naming:%v input:%q, want true/dev", c.layoutsNaming, c.layoutsInput)
	}
	s.handleLayoutsMenuInput(c, []byte("\r"))
	s.handleLayoutsMenuInput(c, []byte("y"))
	if len(c.layoutsList) != 1 || !strings.Contains(c.layoutsMessage, "Updated") {
		t.Fatalf("after overwrite: list=%d message=%q", len(c.layoutsList), c.layoutsMessage)
	}

	// Delete needs confirmation too.
	s.handleLayoutsMenuInput(c, []byte("d"))
	if c.layoutsConfirmDelete != "dev" {
		t.Fatalf("delete confirmation = %q, want dev", c.layoutsConfirmDelete)
	}
	if !strings.Contains(c.layoutsMessage, "Delete layout") {
		t.Fatalf("delete message = %q", c.layoutsMessage)
	}
	s.handleLayoutsMenuInput(c, []byte("y"))
	if len(c.layoutsList) != 0 {
		t.Fatalf("after delete: list=%d, want 0", len(c.layoutsList))
	}
	if list, warning, _ := loadLayouts(s.layoutsPath); warning != nil || len(list) != 0 {
		t.Fatalf("after delete file = %+v warning=%+v", list, warning)
	}
}

func TestLayoutsMenuAutoNameAndInputSwallowing(t *testing.T) {
	s, c := newHelpServer(80, 24)
	s.layoutsPath = filepath.Join(t.TempDir(), "layouts.toml")
	f, err := os.CreateTemp(t.TempDir(), "pane-input")
	if err != nil {
		t.Fatalf("temp pane input: %v", err)
	}
	defer f.Close()
	s.sess.activePane().ptmx = f

	fireBinding(s, c, "L")
	s.handleInput(c, []byte("echo should-not-run\n"))
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read pane input: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("menu key reached pane: %q", raw)
	}

	// Empty name uses layout-1.
	s.handleLayoutsMenuInput(c, []byte("s"))
	s.handleLayoutsMenuInput(c, []byte("\r"))
	if len(c.layoutsList) != 1 || c.layoutsList[0].Name != "layout-1" {
		t.Fatalf("auto name = %+v", c.layoutsList)
	}
}

func TestLayoutsMenuMoveClampsAndClearsPrompt(t *testing.T) {
	s, c := newHelpServer(80, 24)
	c.layoutsOpen = true
	c.layoutsList = layoutsMenuFixture()
	c.layoutsCursor = 0
	c.layoutsMessage = "status"
	s.layoutsMenuMoveCursor(c, -5)
	if c.layoutsCursor != 0 || c.layoutsMessage != "" {
		t.Fatalf("cursor=%d message=%q, want 0/empty", c.layoutsCursor, c.layoutsMessage)
	}
	s.layoutsMenuMoveCursor(c, 99)
	if c.layoutsCursor != len(c.layoutsList) {
		t.Fatalf("cursor = %d, want save row %d", c.layoutsCursor, len(c.layoutsList))
	}
}
