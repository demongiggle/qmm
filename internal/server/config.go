package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// configWarning describes a config failure that must be shown to the user.
// BackupPath is empty when the broken file could not be preserved.
type configWarning struct {
	Reason     string
	BackupPath string
	ConfigPath string
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
	if err := encodeSettings(f, st); err != nil {
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

	st, err := decodeSettings(path)
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
