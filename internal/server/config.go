package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// configWarning describes a config failure that must be shown to the user.
// BackupPath is empty when the broken file could not be preserved.
type configWarning struct {
	Reason     string
	BackupPath string
	ConfigPath string
}

// fileConfig is the on-disk TOML representation of every settings-page row.
// Values are the same human-readable strings shown by the settings UI.
type fileConfig struct {
	Appearance appearanceConfig `toml:"appearance"`
	Panes      panesConfig      `toml:"panes"`
	Layout     layoutConfig     `toml:"layout"`
	Session    sessionConfig    `toml:"session"`
	Copy       copyConfig       `toml:"copy"`
}

type appearanceConfig struct {
	StatusBarClock   string `toml:"status_bar_clock"`
	StatusBarColor   string `toml:"status_bar_color"`
	ActivePaneBorder string `toml:"active_pane_border"`
	CursorShape      string `toml:"cursor_shape"`
	CursorBlink      string `toml:"cursor_blink"`
}

type panesConfig struct {
	PaneNumberTimeout string `toml:"pane_number_timeout"`
	DoubleClickZoom   string `toml:"double_click_zoom"`
}

type layoutConfig struct {
	KeyboardResizeStep   string `toml:"keyboard_resize_step"`
	AutomaticWindowNames string `toml:"automatic_window_names"`
}

type sessionConfig struct {
	ConfirmShutdown string `toml:"confirm_shutdown"`
}

type copyConfig struct {
	StatusLineColor string `toml:"status_line_color"`
	CopyKey         string `toml:"copy_key"`
}

type configSetting struct {
	id    settingID
	value string
}

func configFilePath() (string, error) {
	if path := os.Getenv("QMM_CONFIG"); path != "" {
		return filepath.Clean(path), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolve config directory: %w", errors.Join(err, homeErr))
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "qmm", "config.toml"), nil
}

func settingText(st *settingsState, id settingID) string {
	def, ok := settingByID(id)
	if !ok {
		return ""
	}
	return st.text(def)
}

func configFromSettings(st *settingsState) fileConfig {
	st.ensure()
	return fileConfig{
		Appearance: appearanceConfig{
			StatusBarClock:   settingText(st, settingStatusClock),
			StatusBarColor:   settingText(st, settingStatusBarColor),
			ActivePaneBorder: settingText(st, settingBorderColor),
			CursorShape:      settingText(st, settingCursorShape),
			CursorBlink:      settingText(st, settingCursorBlink),
		},
		Panes: panesConfig{
			PaneNumberTimeout: settingText(st, settingPaneNumberTime),
			DoubleClickZoom:   settingText(st, settingDoubleClickZoom),
		},
		Layout: layoutConfig{
			KeyboardResizeStep:   settingText(st, settingResizeStep),
			AutomaticWindowNames: settingText(st, settingAutoNameWindows),
		},
		Session: sessionConfig{
			ConfirmShutdown: settingText(st, settingConfirmShutdown),
		},
		Copy: copyConfig{
			StatusLineColor: settingText(st, settingCopyStatusColor),
			CopyKey:         settingText(st, settingCopyKey),
		},
	}
}

func (fc fileConfig) orderedSettings() []configSetting {
	return []configSetting{
		{settingStatusClock, fc.Appearance.StatusBarClock},
		{settingStatusBarColor, fc.Appearance.StatusBarColor},
		{settingBorderColor, fc.Appearance.ActivePaneBorder},
		{settingCursorShape, fc.Appearance.CursorShape},
		{settingCursorBlink, fc.Appearance.CursorBlink},
		{settingPaneNumberTime, fc.Panes.PaneNumberTimeout},
		{settingDoubleClickZoom, fc.Panes.DoubleClickZoom},
		{settingResizeStep, fc.Layout.KeyboardResizeStep},
		{settingAutoNameWindows, fc.Layout.AutomaticWindowNames},
		{settingConfirmShutdown, fc.Session.ConfirmShutdown},
		{settingCopyStatusColor, fc.Copy.StatusLineColor},
		{settingCopyKey, fc.Copy.CopyKey},
	}
}

func settingsFromConfig(fc fileConfig) (settingsState, error) {
	st := defaultSettings()
	for _, entry := range fc.orderedSettings() {
		// Missing keys keep their defaults; unknown TOML keys are ignored by
		// the decoder and never reach this loop.
		if entry.value == "" {
			continue
		}
		def, ok := settingByID(entry.id)
		if !ok {
			continue
		}
		idx := -1
		for i, allowed := range def.Values {
			if allowed == entry.value {
				idx = i
				break
			}
		}
		if idx < 0 {
			return st, fmt.Errorf("invalid value %q for %q", entry.value, entry.id)
		}
		st.values[entry.id] = idx
	}
	return st, nil
}

// writeConfig atomically replaces path with the complete config. Callers keep
// their in-memory settings when this fails, so a partial write can never make
// the live session disagree with itself.
func writeConfig(path string, st *settingsState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".qmm-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(configFromSettings(st)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func recoverBrokenConfig(path, reason string) *configWarning {
	backupPath := fmt.Sprintf("%s.%s.bak", path, time.Now().Format("20060102-150405.000000000"))
	if err := os.Rename(path, backupPath); err != nil {
		return &configWarning{
			Reason:     fmt.Sprintf("%s; could not create backup: %v", reason, err),
			ConfigPath: path,
		}
	}
	defaults := defaultSettings()
	if err := writeConfig(path, &defaults); err != nil {
		return &configWarning{
			Reason:     fmt.Sprintf("%s; could not write default config: %v", reason, err),
			BackupPath: backupPath,
			ConfigPath: path,
		}
	}
	return &configWarning{
		Reason:     reason,
		BackupPath: backupPath,
		ConfigPath: path,
	}
}

// loadConfig creates a default file when absent, loads valid values, and
// recovers from broken files by preserving them and restoring defaults.
func loadConfig(path string) (settingsState, *configWarning) {
	defaults := defaultSettings()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return defaults, &configWarning{
			Reason:     fmt.Sprintf("could not create config directory: %v", err),
			ConfigPath: path,
		}
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeConfig(path, &defaults); err != nil {
			return defaults, &configWarning{
				Reason:     fmt.Sprintf("could not create default config: %v", err),
				ConfigPath: path,
			}
		}
		return defaults, nil
	} else if err != nil {
		return defaults, recoverBrokenConfig(path, fmt.Sprintf("could not read config: %v", err))
	}

	var fc fileConfig
	if _, err := toml.DecodeFile(path, &fc); err != nil {
		return defaults, recoverBrokenConfig(path, fmt.Sprintf("could not parse config: %v", err))
	}
	st, err := settingsFromConfig(fc)
	if err != nil {
		return defaults, recoverBrokenConfig(path, err.Error())
	}
	return st, nil
}

func logConfigWarning(logPath string, warning configWarning) {
	if logPath == "" {
		return
	}
	line := fmt.Sprintf("qmm: config warning: %s; config: %s", warning.Reason, warning.ConfigPath)
	if warning.BackupPath != "" {
		line += fmt.Sprintf("; backup: %s", warning.BackupPath)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, line)
}

// initConfig resolves the config path and applies it before the first frame
// is composed. A failure never prevents the session from starting; it is
// queued as a warning for the first attached client.
func (s *Server) initConfig(logPath string) {
	s.configLogPath = logPath
	path, err := configFilePath()
	if err != nil {
		warning := configWarning{
			Reason:     fmt.Sprintf("could not resolve config path: %v", err),
			ConfigPath: "(unknown)",
		}
		logConfigWarning(s.configLogPath, warning)
		s.pendingConfigWarning = &warning
		return
	}
	s.configPath = path
	st, warning := loadConfig(path)
	s.sess.settings = st
	if warning != nil {
		logConfigWarning(s.configLogPath, *warning)
		s.pendingConfigWarning = warning
	}
}

// persistSettings saves every settings value after a UI change. A failed save
// leaves the live value intact and surfaces a client-local warning.
func (s *Server) persistSettings(c *clientConn) {
	if s == nil || s.configPath == "" {
		return
	}
	if err := writeConfig(s.configPath, s.sess.opts()); err != nil {
		warning := configWarning{
			Reason:     fmt.Sprintf("could not save config: %v", err),
			ConfigPath: s.configPath,
		}
		logConfigWarning(s.configLogPath, warning)
		s.showConfigWarning(c, warning)
	}
}

// reloadConfig applies external edits to all clients. A successful reload
// closes the requester's settings page; a broken file repeats recovery.
func (s *Server) reloadConfig(c *clientConn) {
	if s == nil || s.configPath == "" {
		return
	}
	st, warning := loadConfig(s.configPath)
	s.sess.settings = st
	s.needFull = true
	if warning != nil {
		logConfigWarning(s.configLogPath, *warning)
		s.showConfigWarning(c, *warning)
		c.forceFull = true
		s.broadcast()
		return
	}
	c.forceFull = true
	s.closeSettings(c)
}
