package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/theme"
)

func themeSettingIndex(t *testing.T, id string) int {
	t.Helper()
	for i, candidate := range theme.IDs() {
		if candidate == id {
			return i
		}
	}
	t.Fatalf("theme %q not found", id)
	return 0
}

func setTheme(t *testing.T, st *settingsState, id string) {
	t.Helper()
	if !st.setByID(settingTheme, themeSettingIndex(t, id)) {
		t.Fatalf("setting theme %q failed", id)
	}
}

func TestDefaultChromeMatchesLegacyColors(t *testing.T) {
	c := defaultChrome()
	if c.themed {
		t.Fatal("default chrome marked as themed")
	}
	if c.background != colDefault || c.text != colDefault {
		t.Fatalf("default chrome bg/text = %d/%d, want terminal default", c.background, c.text)
	}
	if c.border != colBorder || c.activeBorder != accentColorCodes[0] || c.activeFG != colBarActiveFG {
		t.Fatalf("default border chrome = %+v", c)
	}
	if c.statusBar != statusBarColors[0] || c.copyStatus != statusBarColors[1] {
		t.Fatalf("default bars = %+v / %+v", c.statusBar, c.copyStatus)
	}
	if c.help.backdrop != colHelpBackdrop || c.help.panelBG != colHelpPanelBG ||
		c.help.border != colHelpBorder || c.help.keyFG != colHelpKeyFG {
		t.Fatalf("default help chrome = %+v", c.help)
	}
	if c.paneNumber.activeBorder != colPaneNumberActiveBord || c.paneNumber.shadowBG != colPaneNumberShadowBG {
		t.Fatalf("default pane-number chrome = %+v", c.paneNumber)
	}
	if c.settingsFG != colSettingsSelectedFG || c.copyCursorBG != colCopyCursorBG {
		t.Fatalf("default overlay chrome = %+v", c)
	}
}

func TestThemeChromeDerivationAndOverrides(t *testing.T) {
	p, ok := theme.Lookup("dracula")
	if !ok {
		t.Fatal("dracula missing")
	}
	c := chromeForPalette(p)
	if !c.themed {
		t.Fatal("named theme not themed")
	}
	if got, want := c.background, protocolRGB(theme.Color(p.Background)); got != want {
		t.Fatalf("background = %#x, want %#x", got, want)
	}
	if got, want := c.text, protocolRGB(theme.Color(p.Text)); got != want {
		t.Fatalf("text = %#x, want %#x", got, want)
	}
	if got, want := c.activeBorder, protocolRGB(theme.Color(p.Accent)); got != want {
		t.Fatalf("active border = %#x, want %#x", got, want)
	}
	if c.statusBar.bg == c.copyStatus.bg {
		t.Fatal("session and copy bars share a theme surface")
	}
	if c.help.backdrop == c.help.panelBG {
		t.Fatal("help backdrop and panel share a theme surface")
	}

	s := newTestSession()
	setTheme(t, s.opts(), "dracula")
	s.opts().setByID(settingStatusBarColor, statusSettingIndex(t, "Navy"))
	s.opts().setByID(settingCopyStatusColor, statusSettingIndex(t, "Green"))
	s.opts().setByID(settingBorderColor, accentSettingIndex(t, "Pink"))
	c = s.opts().chrome()
	if c.statusBar != statusBarColors[statusColorIndex(t, "Navy")] {
		t.Fatalf("status override = %+v", c.statusBar)
	}
	if c.copyStatus != statusBarColors[statusColorIndex(t, "Green")] {
		t.Fatalf("copy override = %+v", c.copyStatus)
	}
	if got, want := c.activeBorder, accentColorCodes[accentColorIndex(t, "Pink")]; got != want {
		t.Fatalf("border override = %#x, want %#x", got, want)
	}
	if c.activeFG != colBarActiveFG {
		t.Fatalf("explicit border label FG = %d, want %d", c.activeFG, colBarActiveFG)
	}
}

func TestThemeColorMapping(t *testing.T) {
	p, _ := theme.Lookup("dracula")
	c := chromeForPalette(p)
	if got, want := c.mapColor(protocol.ColorDefault, true), protocolRGB(theme.Color(p.Text)); got != want {
		t.Fatalf("default fg = %#x, want %#x", got, want)
	}
	if got, want := c.mapColor(protocol.ColorDefault, false), protocolRGB(theme.Color(p.Background)); got != want {
		t.Fatalf("default bg = %#x, want %#x", got, want)
	}
	if got, want := c.mapColor(1, true), protocolRGB(p.ANSI256[1]); got != want {
		t.Fatalf("ANSI 1 = %#x, want %#x", got, want)
	}
	if got, want := c.mapColor(196, false), protocolRGB(p.ANSI256[196]); got != want {
		t.Fatalf("ANSI 196 = %#x, want %#x", got, want)
	}
	rgb := protocol.RGBColor(18, 52, 86)
	if got := c.mapColor(rgb, true); got != rgb {
		t.Fatalf("truecolor changed: %#x", got)
	}
	if got := defaultChrome().mapColor(196, true); got != 196 {
		t.Fatalf("default palette mapped 196 to %#x", got)
	}
}

func TestComposeAppliesThemeToPaneAndChrome(t *testing.T) {
	s := newTestSession()
	setTheme(t, s.opts(), "dracula")
	p, _ := theme.Lookup("dracula")
	if _, err := s.panes[1].vt.Write([]byte("\x1b[38;5;196mR\x1b[38;2;18;52;86mT")); err != nil {
		t.Fatal(err)
	}
	f := (&clientView{}).compose(s, 80, 24, true)

	if got, want := cellAt(f, 0, 0).FG, protocolRGB(p.ANSI256[196]); got != want {
		t.Errorf("pane ANSI 196 FG = %#x, want %#x", got, want)
	}
	if got, want := cellAt(f, 1, 0).FG, protocol.RGBColor(18, 52, 86); got != want {
		t.Errorf("pane truecolor FG = %#x, want %#x", got, want)
	}
	if got, want := cellAt(f, 10, 0).BG, protocolRGB(theme.Color(p.Background)); got != want {
		t.Errorf("blank pane BG = %#x, want theme background %#x", got, want)
	}
	if got, want := cellAt(f, 10, 0).FG, protocolRGB(theme.Color(p.Text)); got != want {
		t.Errorf("blank pane FG = %#x, want theme text %#x", got, want)
	}
	if got, want := cellAt(f, 40, 5).FG, protocolRGB(theme.Color(p.Accent)); got != want {
		t.Errorf("active border FG = %#x, want accent %#x", got, want)
	}
	if got, want := cellAt(f, 60, 23).BG, chromeForPalette(p).statusBar.bg; got != want {
		t.Errorf("status bar BG = %#x, want %#x", got, want)
	}
}

func TestThemeAppliesToOverlaysAndCopyMode(t *testing.T) {
	s, c := newHelpServer(80, 24)
	setTheme(t, s.sess.opts(), "dracula")
	p, _ := theme.Lookup("dracula")
	ch := chromeForPalette(p)

	help := (&clientView{menuView: menuView{menu: menuHelp}}).compose(s.sess, 80, 24, true)
	x0, y0, _, _ := helpBox(80, 24)
	if got, want := cellAt(help, x0, y0).FG, ch.help.border; got != want {
		t.Errorf("help border FG = %#x, want %#x", got, want)
	}
	if got, want := cellAt(help, x0+2, y0+1).BG, ch.help.panelBG; got != want {
		t.Errorf("help panel BG = %#x, want %#x", got, want)
	}

	confirm := (&clientView{confirmOpen: true}).compose(s.sess, 80, 24, true)
	x0, y0, _, _ = shutdownConfirmBox(80, 24)
	if got, want := cellAt(confirm, x0+1, y0).BG, ch.help.panelBG; got != want {
		t.Errorf("confirm panel BG = %#x, want %#x", got, want)
	}

	numbers := (&clientView{displayPanesOpen: true}).compose(s.sess, 80, 24, true)
	found := false
	for _, cell := range numbers.Cells {
		if cell.Ch == '█' && (cell.BG == ch.paneNumber.activeBG || cell.BG == ch.paneNumber.panelBG) {
			found = true
			break
		}
	}
	if !found {
		t.Error("pane number overlay did not use the theme palette")
	}

	// Copy mode's cursor and selection highlights come from the theme too.
	st := &copyModeState{open: true, paneID: 1, lines: []copyLine{lineOf("hello")}, rect: s.paneRects()[1]}
	st.cursor.line = 0
	st.top = 0
	c.copyMode = st
	s.broadcast()
	cur := cellAt(*c.frame, st.rect.X, st.rect.Y)
	if cur.BG != ch.copyCursorBG || cur.FG != ch.copyCursorFG {
		t.Errorf("copy cursor = %#x/%#x, want %#x/%#x", cur.FG, cur.BG, ch.copyCursorFG, ch.copyCursorBG)
	}
}

func TestEveryThemeChromeIsDistinctAndReadable(t *testing.T) {
	for _, p := range theme.All()[1:] {
		c := chromeForPalette(p)
		if c.statusBar.bg == c.copyStatus.bg {
			t.Errorf("%s session/copy bars share BG %#x", p.ID, c.statusBar.bg)
		}
		if c.help.backdrop == c.help.panelBG {
			t.Errorf("%s help backdrop/panel share BG %#x", p.ID, c.help.panelBG)
		}
		if contrastRatio(c.statusBar.fg, c.statusBar.bg) < 3.0 {
			t.Errorf("%s status bar contrast too low", p.ID)
		}
		if contrastRatio(c.copyStatus.fg, c.copyStatus.bg) < 3.0 {
			t.Errorf("%s copy bar contrast too low", p.ID)
		}
		if contrastRatio(c.help.fg, c.help.panelBG) < 3.0 {
			t.Errorf("%s help text contrast too low", p.ID)
		}
	}
}

func TestThemeConfigRoundTripMigrationAndValidation(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		st := defaultSettings()
		setTheme(t, &st, "dracula")
		if err := writeConfig(path, &st); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `theme = "dracula"`) {
			t.Fatalf("saved config missing theme:\n%s", raw)
		}
		loaded, warning := loadConfig(path)
		if warning != nil {
			t.Fatalf("load warning: %+v", *warning)
		}
		if got := loaded.themePalette().ID; got != "dracula" {
			t.Fatalf("loaded theme = %q, want dracula", got)
		}
	})

	t.Run("old defaults migrate", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		body := `[appearance]
status_bar_color = "Slate"
active_pane_border = "Green"

[copy]
status_line_color = "Blue"
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		st, warning := loadConfig(path)
		if warning != nil {
			t.Fatalf("load warning: %+v", *warning)
		}
		for _, id := range []settingID{settingStatusBarColor, settingCopyStatusColor, settingBorderColor} {
			if _, ok := st.paletteOverride(id); ok {
				t.Errorf("%s still has an explicit override", id)
			}
		}
	})

	t.Run("custom values stay explicit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		body := `[appearance]
status_bar_color = "Navy"
active_pane_border = "Pink"

[copy]
status_line_color = "Olive"
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		st, warning := loadConfig(path)
		if warning != nil {
			t.Fatalf("load warning: %+v", *warning)
		}
		if got, ok := st.paletteOverride(settingStatusBarColor); !ok || got != statusColorIndex(t, "Navy") {
			t.Errorf("status override = %d/%v", got, ok)
		}
		if got, ok := st.paletteOverride(settingBorderColor); !ok || got != accentColorIndex(t, "Pink") {
			t.Errorf("border override = %d/%v", got, ok)
		}
		if got, ok := st.paletteOverride(settingCopyStatusColor); !ok || got != statusColorIndex(t, "Olive") {
			t.Errorf("copy override = %d/%v", got, ok)
		}
	})

	t.Run("invalid theme recovers", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[appearance]\ntheme = \"not-a-theme\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, warning := loadConfig(path)
		if warning == nil {
			t.Fatal("invalid theme did not produce a config warning")
		}
		var fc map[string]map[string]string
		if _, err := toml.DecodeFile(path, &fc); err != nil {
			t.Fatalf("recovered config invalid: %v", err)
		}
		if got := st.themePalette().ID; got != "default" {
			t.Fatalf("fallback theme = %q, want default", got)
		}
		if fc["appearance"]["theme"] != "default" {
			t.Fatalf("recovered config theme = %q, want default", fc["appearance"]["theme"])
		}
	})
}

func TestSettingsCycleThemeLiveAndPersist(t *testing.T) {
	s, c := newHelpServer(80, 24)
	dir := t.TempDir()
	s.configPath = filepath.Join(dir, "config.toml")
	s.configLogPath = filepath.Join(dir, "server.log")
	fireBinding(s, c, "s")
	if c.settingsCursor != settingCursorIndex(t, settingTheme) {
		t.Fatalf("settings opened at cursor %d, want Theme", c.settingsCursor)
	}
	s.handleInput(c, []byte("\x1b[C"))
	if c.configWarningOpen {
		t.Fatalf("theme save warned: %+v", c.configWarning)
	}
	if got := s.sess.opts().themePalette().ID; got != "catppuccin-mocha" {
		t.Fatalf("selected theme = %q", got)
	}
	if !strings.Contains(frameText(*c.frame), "Catppuccin Mocha · Dark") {
		t.Fatalf("settings frame missing theme label:\n%s", frameText(*c.frame))
	}
	p := s.sess.opts().themePalette()
	want := chromeForPalette(p).statusBar
	if got := cellAt(*c.frame, 0, 23).BG; got != want.bg {
		t.Fatalf("status bar BG = %#x, want theme %#x", got, want.bg)
	}
	var fc map[string]map[string]string
	if _, err := toml.DecodeFile(s.configPath, &fc); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if fc["appearance"]["theme"] != "catppuccin-mocha" {
		t.Fatalf("saved theme = %q", fc["appearance"]["theme"])
	}
}
