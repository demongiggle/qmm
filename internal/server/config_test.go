package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/demongiggle/qmm/internal/protocol"
)

func TestConfigFilePathOverrideAndXDG(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom.toml")
	t.Setenv("QMM_CONFIG", custom)
	got, err := configFilePath()
	if err != nil || got != custom {
		t.Fatalf("QMM_CONFIG path = %q, %v; want %q", got, err, custom)
	}

	t.Setenv("QMM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err = configFilePath()
	want := filepath.Join(dir, "qmm", "config.toml")
	if err != nil || got != want {
		t.Fatalf("XDG config path = %q, %v; want %q", got, err, want)
	}
}

func TestLoadConfigCreatesDefaultsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	st, warning := loadConfig(path)
	if warning != nil {
		t.Fatalf("missing config produced warning: %+v", *warning)
	}
	if got := copyKeySequence(&st); got != "\x19" {
		t.Fatalf("default copy key = %q, want the Ctrl-Y sequence", got)
	}
	if st.cursorShape() != protocol.CursorShapeBlock || st.cursorBlink() {
		t.Fatal("missing config did not use a steady block")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read default config: %v", err)
	}
	for _, key := range []string{
		"theme", "status_bar_clock", "status_bar_color", "active_pane_border", "cursor_shape", "cursor_blink",
		"pane_number_timeout", "double_click_zoom", "keyboard_resize_step",
		"automatic_window_names", "confirm_shutdown", "status_line_color", "copy_key",
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("default config missing key %q:\n%s", key, raw)
		}
	}

	st.setByID(settingCursorShape, 2)
	st.setByID(settingCursorBlink, 0)
	st.setByID(settingBorderColor, accentSettingIndex(t, "Pink"))
	st.setByID(settingCopyKey, 1)                                      // Ctrl-Shift-X, away from the Ctrl-Y default
	st.setByID(settingCopyStatusColor, statusSettingIndex(t, "Green")) // away from the Blue default
	st.setByID(settingStatusBarColor, statusSettingIndex(t, "Navy"))
	if err := writeConfig(path, &st); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, warning := loadConfig(path)
	if warning != nil {
		t.Fatalf("valid config produced warning: %+v", *warning)
	}
	if got := copyKeySequence(&loaded); got != "\x1b[27;6;120~" {
		t.Fatalf("loaded copy key = %q, want Ctrl-Shift-X sequence", got)
	}
	if loaded.cursorShape() != protocol.CursorShapeBar || !loaded.cursorBlink() {
		t.Fatalf("loaded cursor = shape:%d blink:%v, want blinking bar", loaded.cursorShape(), loaded.cursorBlink())
	}
	if got, want := loaded.borderColor(), accentColorCodes[accentColorIndex(t, "Pink")]; got != want {
		t.Fatalf("loaded border color = %d, want the Pink accent %d", got, want)
	}
	if got, want := loaded.copyStatusStyle(), statusBarColors[statusColorIndex(t, "Green")]; got != want {
		t.Fatalf("loaded copy status style = %+v, want %+v", got, want)
	}
	if got, want := loaded.statusBarStyle(), statusBarColors[statusColorIndex(t, "Navy")]; got != want {
		t.Fatalf("loaded status bar style = %+v, want %+v", got, want)
	}
}

func TestLoadConfigIgnoresUnknownAndMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[appearance]\nunknown_key = \"ignored\"\ncursor_shape = \"Bar\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write partial config: %v", err)
	}
	st, warning := loadConfig(path)
	if warning != nil {
		t.Fatalf("partial config produced warning: %+v", *warning)
	}
	if st.cursorShape() != protocol.CursorShapeBar {
		t.Fatalf("cursor shape = %d, want bar", st.cursorShape())
	}
	if st.cursorBlink() || !st.showClock() {
		t.Fatal("missing keys should keep their defaults")
	}
}

func TestLoadConfigRecoversBrokenFiles(t *testing.T) {
	cases := map[string]string{
		"parse error":   "[appearance\ncursor_shape = \"Block\"\n",
		"invalid value": "[appearance]\ncursor_shape = \"Triangle\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write broken config: %v", err)
			}
			st, warning := loadConfig(path)
			if warning == nil {
				t.Fatal("broken config did not produce a warning")
			}
			if warning.BackupPath == "" || warning.ConfigPath != path {
				t.Fatalf("warning paths = backup:%q config:%q", warning.BackupPath, warning.ConfigPath)
			}
			backup, err := os.ReadFile(warning.BackupPath)
			if err != nil || string(backup) != body {
				t.Fatalf("backup = %q, %v; want original broken file", backup, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read recovered config: %v", err)
			}
			var fc fileConfig
			if _, err := toml.Decode(string(raw), &fc); err != nil {
				t.Fatalf("recovered config is not valid TOML: %v", err)
			}
			if fc.Appearance.CursorShape != "Block" || fc.Appearance.CursorBlink != "Off" {
				t.Fatalf("recovered defaults = shape:%q blink:%q", fc.Appearance.CursorShape, fc.Appearance.CursorBlink)
			}
			if st.cursorShape() != protocol.CursorShapeBlock || st.cursorBlink() {
				t.Fatal("broken config did not fall back to a steady block")
			}
		})
	}
}

func TestSettingsAdjustAutoSavesAllValues(t *testing.T) {
	s, c := newHelpServer(80, 24)
	dir := t.TempDir()
	s.configPath = filepath.Join(dir, "config.toml")
	s.configLogPath = filepath.Join(dir, "server.log")
	fireBinding(s, c, "s")
	c.settingsCursor = settingCursorIndex(t, settingCursorShape)
	s.handleInput(c, []byte("\x1b[C"))
	if c.configWarningOpen {
		t.Fatalf("auto-save unexpectedly warned: %+v", c.configWarning)
	}
	var fc fileConfig
	if _, err := toml.DecodeFile(s.configPath, &fc); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if fc.Appearance.CursorShape != "Underline" {
		t.Fatalf("saved cursor shape = %q, want Underline", fc.Appearance.CursorShape)
	}
	for _, value := range fc.orderedSettings() {
		if value.value == "" {
			t.Errorf("saved config missing value for %q", value.id)
		}
	}
}

func TestReloadKeysApplyExternalConfigAndCloseSettings(t *testing.T) {
	for _, key := range []string{"r", "\x12"} {
		t.Run(keyName(key), func(t *testing.T) {
			s, c := newHelpServer(80, 24)
			dir := t.TempDir()
			s.configPath = filepath.Join(dir, "config.toml")
			s.configLogPath = filepath.Join(dir, "server.log")
			st := defaultSettings()
			if err := writeConfig(s.configPath, &st); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			fireBinding(s, c, "s")
			body := "[appearance]\ncursor_shape = \"Bar\"\ncursor_blink = \"On\"\n"
			if err := os.WriteFile(s.configPath, []byte(body), 0o600); err != nil {
				t.Fatalf("edit config: %v", err)
			}
			s.handleInput(c, []byte(key))
			if c.menu == menuSettings {
				t.Error("successful reload did not close the settings page")
			}
			if c.configWarningOpen {
				t.Fatalf("successful reload produced warning: %+v", c.configWarning)
			}
			if got := s.sess.opts().cursorShape(); got != protocol.CursorShapeBar {
				t.Errorf("reloaded cursor shape = %d, want bar", got)
			}
			if !s.sess.opts().cursorBlink() {
				t.Error("reloaded cursor blink = false, want true")
			}
		})
	}
}

func TestBrokenReloadRecoversAndWarnsTriggeringClient(t *testing.T) {
	s, c := newHelpServer(80, 24)
	dir := t.TempDir()
	s.configPath = filepath.Join(dir, "config.toml")
	s.configLogPath = filepath.Join(dir, "server.log")
	st := defaultSettings()
	if err := writeConfig(s.configPath, &st); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fireBinding(s, c, "s")
	if err := os.WriteFile(s.configPath, []byte("not valid toml = ["), 0o600); err != nil {
		t.Fatalf("break config: %v", err)
	}
	s.handleInput(c, []byte("r"))
	if !c.configWarningOpen {
		t.Fatal("broken reload did not show the warning modal")
	}
	if c.menu != menuSettings {
		t.Error("failed reload closed the settings page")
	}
	if c.configWarning.BackupPath == "" {
		t.Fatalf("warning lacks backup path: %+v", c.configWarning)
	}
	if _, err := os.Stat(c.configWarning.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	log, err := os.ReadFile(s.configLogPath)
	if err != nil || !strings.Contains(string(log), "config warning") {
		t.Fatalf("server log = %q, %v; want config warning", log, err)
	}
}

func TestSaveFailureKeepsValueAndWarnsOnlyTriggeringClient(t *testing.T) {
	s, c := newHelpServer(80, 24)
	c2 := newHelpClient()
	s.clients[c2] = struct{}{}
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	s.configPath = filepath.Join(blocker, "config.toml")
	s.configLogPath = filepath.Join(dir, "server.log")
	fireBinding(s, c, "s")
	c.settingsCursor = settingCursorIndex(t, settingCursorShape)
	s.handleInput(c, []byte("\x1b[C"))
	if !c.configWarningOpen {
		t.Fatal("save failure did not show a warning modal")
	}
	if c2.configWarningOpen {
		t.Error("save failure warned a different client")
	}
	if got := s.sess.opts().cursorShape(); got != protocol.CursorShapeUnderline {
		t.Errorf("in-memory cursor shape = %d, want underline", got)
	}
	log, err := os.ReadFile(s.configLogPath)
	if err != nil || !strings.Contains(string(log), "could not save config") {
		t.Fatalf("server log = %q, %v; want save failure", log, err)
	}
}

func keyName(key string) string {
	if key == "\x12" {
		return "ctrl-r"
	}
	return key
}
