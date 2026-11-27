package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/demongiggle/qmm/internal/layout"
	"github.com/demongiggle/qmm/internal/protocol"
)

const (
	layoutFileVersion = 1
	maxLayoutNameLen  = 32
)

// savedLayout is one reusable window template. Panes are stored in the same
// depth-first order as the tree's leaf indexes.
type savedLayout struct {
	Name  string      `toml:"name"`
	Tree  layout.Spec `toml:"tree"`
	Panes []savedPane `toml:"panes"`
}

// savedPane records where a pane started and, when it was busy, the foreground
// process argv to run again. An empty Command means a login shell.
type savedPane struct {
	CWD     string   `toml:"cwd,omitempty"`
	Command []string `toml:"command,omitempty"`
}

type layoutFile struct {
	Version int           `toml:"version"`
	Layouts []savedLayout `toml:"layouts"`
}

// initLayouts resolves the layouts file beside config.toml and loads it once at
// startup. A failure never prevents the server from starting; the menu reports
// it and disables destructive writes when the file cannot be recovered.
func (s *Server) initLayouts() {
	if s.configPath == "" {
		s.layoutsErr = "could not resolve config path for layouts file"
		s.layoutsReadOnly = true
		return
	}
	s.layoutsPath = filepath.Join(filepath.Dir(s.configPath), "layouts.toml")
	s.reloadLayouts()
}

// reloadLayouts refreshes the in-memory list from disk. Existing in-memory
// layouts are replaced only by a successful read so a transient I/O failure
// does not silently clear the menu.
func (s *Server) reloadLayouts() *configWarning {
	if s.layoutsPath == "" {
		s.layoutsErr = "layouts path unavailable"
		s.layoutsReadOnly = true
		return nil
	}
	list, warning, readOnly := loadLayouts(s.layoutsPath)
	s.layoutsReadOnly = readOnly
	if warning != nil {
		logConfigWarning(s.configLogPath, *warning)
		s.layoutsErr = layoutWarningMessage(*warning)
	} else {
		s.layoutsErr = ""
	}
	if !readOnly {
		s.layouts = list
	}
	return warning
}

func layoutWarningMessage(w configWarning) string {
	msg := w.Reason
	if w.BackupPath != "" {
		msg += "; backup: " + w.BackupPath
	}
	return msg
}

// loadLayouts reads and validates every entry before any pane is spawned.
// Malformed or invalid files are moved aside so user data survives; a newer
// file version is left untouched and puts the menu into read-only mode.
func loadLayouts(path string) ([]savedLayout, *configWarning, bool) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false
	}
	if err != nil {
		warning := recoverLayouts(path, fmt.Sprintf("could not read layouts: %v", err))
		return nil, warning, warning.BackupPath == ""
	}

	var lf layoutFile
	if _, err := toml.Decode(string(b), &lf); err != nil {
		warning := recoverLayouts(path, fmt.Sprintf("could not parse layouts: %v", err))
		return nil, warning, warning.BackupPath == ""
	}
	if lf.Version > layoutFileVersion {
		return nil, &configWarning{
			Reason:     fmt.Sprintf("layouts file version %d is newer than supported version %d; not loading it", lf.Version, layoutFileVersion),
			ConfigPath: path,
		}, true
	}
	if err := validateSavedLayouts(lf.Layouts); err != nil {
		warning := recoverLayouts(path, fmt.Sprintf("invalid layouts file: %v", err))
		return nil, warning, warning.BackupPath == ""
	}
	sortLayouts(lf.Layouts)
	return lf.Layouts, nil, false
}

func recoverLayouts(path, reason string) *configWarning {
	backupPath := fmt.Sprintf("%s.%s.bak", path, time.Now().Format("20060102-150405.000000000"))
	if err := os.Rename(path, backupPath); err != nil {
		return &configWarning{
			Reason:     fmt.Sprintf("%s; could not create backup: %v", reason, err),
			ConfigPath: path,
		}
	}
	return &configWarning{Reason: reason, BackupPath: backupPath, ConfigPath: path}
}

func validateSavedLayouts(list []savedLayout) error {
	seen := make(map[string]bool, len(list))
	for i := range list {
		l := &list[i]
		name := strings.TrimSpace(l.Name)
		if err := validateLayoutName(name); err != nil {
			return fmt.Errorf("layout %d: %w", i+1, err)
		}
		l.Name = name
		if seen[name] {
			return fmt.Errorf("duplicate layout name %q", name)
		}
		seen[name] = true
		if len(l.Panes) == 0 {
			return fmt.Errorf("layout %q has no panes", name)
		}
		ids := make([]int, len(l.Panes))
		for j := range ids {
			ids[j] = j
		}
		if _, err := layout.FromSpec(l.Tree, ids); err != nil {
			return fmt.Errorf("layout %q: %w", name, err)
		}
		for j := range l.Panes {
			cmd := l.Panes[j].Command
			if len(cmd) > 0 {
				cleaned := cleanCommandArgv(cmd)
				if cleaned == nil {
					return fmt.Errorf("layout %q pane %d has an invalid command", name, j+1)
				}
				l.Panes[j].Command = cleaned
			}
			if !utf8.ValidString(l.Panes[j].CWD) {
				return fmt.Errorf("layout %q pane %d has an invalid working directory", name, j+1)
			}
		}
	}
	return nil
}

func validateLayoutName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > maxLayoutNameLen {
		return fmt.Errorf("name is longer than %d characters", maxLayoutNameLen)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("name %q may only use letters, digits, '.', '_' and '-'", name)
		}
	}
	return nil
}

func sortLayouts(list []savedLayout) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Name < list[j].Name
	})
}

func layoutIndex(list []savedLayout, name string) int {
	for i := range list {
		if list[i].Name == name {
			return i
		}
	}
	return -1
}

func nextLayoutName(list []savedLayout) string {
	used := make(map[string]bool, len(list))
	for _, l := range list {
		used[l.Name] = true
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("layout-%d", i)
		if !used[name] {
			return name
		}
	}
}

// writeLayouts atomically replaces path with a complete layouts file.
func writeLayouts(path string, list []savedLayout) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".qmm-layouts-*.tmp")
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
	sorted := append([]savedLayout(nil), list...)
	sortLayouts(sorted)
	if err := toml.NewEncoder(f).Encode(layoutFile{Version: layoutFileVersion, Layouts: sorted}); err != nil {
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

// saveCurrentLayout snapshots the active window and persists it under name.
// The returned existed flag reports whether an existing layout was replaced.
func (s *Server) saveCurrentLayout(name string) (savedLayout, bool, error) {
	name = strings.TrimSpace(name)
	if err := validateLayoutName(name); err != nil {
		return savedLayout{}, false, err
	}
	w := s.activeWindow()
	if w == nil || w.tree == nil {
		return savedLayout{}, false, errors.New("no active window")
	}
	ids := w.tree.Panes()
	if len(ids) == 0 {
		return savedLayout{}, false, errors.New("active window has no panes")
	}
	saved := savedLayout{
		Name:  name,
		Tree:  w.tree.Spec(),
		Panes: make([]savedPane, len(ids)),
	}
	for i, id := range ids {
		p := s.sess.panes[id]
		if p == nil {
			continue
		}
		argv, cwd, isShell := foregroundProcess(p)
		if cwd != "" && utf8.ValidString(cwd) {
			saved.Panes[i].CWD = cwd
		}
		if !isShell {
			saved.Panes[i].Command = argv
		}
	}

	candidate := append([]savedLayout(nil), s.layouts...)
	existed := false
	if idx := layoutIndex(candidate, name); idx >= 0 {
		candidate[idx] = saved
		existed = true
	} else {
		candidate = append(candidate, saved)
	}
	if err := s.persistLayouts(candidate); err != nil {
		return savedLayout{}, existed, err
	}
	return saved, existed, nil
}

func (s *Server) deleteSavedLayout(name string) error {
	candidate := append([]savedLayout(nil), s.layouts...)
	idx := layoutIndex(candidate, name)
	if idx < 0 {
		return fmt.Errorf("layout %q not found", name)
	}
	candidate = append(candidate[:idx], candidate[idx+1:]...)
	return s.persistLayouts(candidate)
}

func (s *Server) persistLayouts(list []savedLayout) error {
	if s.layoutsPath == "" {
		return errors.New("layouts path unavailable")
	}
	if s.layoutsReadOnly {
		return errors.New("layouts file is read-only")
	}
	if err := writeLayouts(s.layoutsPath, list); err != nil {
		return err
	}
	sortLayouts(list)
	s.layouts = list
	s.layoutsErr = ""
	return nil
}

// restoreLayout builds a new window from a saved layout. Existing windows are
// left untouched. Commands that fail to start fall back to a shell in the saved
// cwd and produce a logged warning plus a client-local bell.
func (s *Server) restoreLayout(c *clientConn, l savedLayout) error {
	dummyIDs := make([]int, len(l.Panes))
	for i := range dummyIDs {
		dummyIDs[i] = i
	}
	if _, err := layout.FromSpec(l.Tree, dummyIDs); err != nil {
		return err
	}

	cols, rows := s.cols, s.rows-statusRows
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	ids := make([]int, 0, len(l.Panes))
	cleanup := func() {
		for _, id := range ids {
			if p := s.sess.panes[id]; p != nil {
				p.kill()
				delete(s.sess.panes, id)
			}
		}
	}
	var warnings []string
	for i, sp := range l.Panes {
		p, err := s.sess.spawnPaneCommand(sp.CWD, sp.Command, cols, rows)
		if err != nil && len(sp.Command) > 0 {
			warnings = append(warnings, fmt.Sprintf("layout %q pane %d: %v", l.Name, i+1, err))
			p, err = s.sess.spawnPaneCommand(sp.CWD, nil, cols, rows)
		}
		if err != nil {
			cleanup()
			return fmt.Errorf("restore layout %q: %w", l.Name, err)
		}
		ids = append(ids, p.id)
	}

	tree, err := layout.FromSpec(l.Tree, ids)
	if err != nil {
		cleanup()
		return fmt.Errorf("restore layout %q: %w", l.Name, err)
	}
	idx := s.sess.lowestFreeIndex()
	w := &window{
		index:      idx,
		tree:       tree,
		activePane: ids[0],
		name:       l.Name,
		pinnedName: true,
	}
	s.sess.windows[idx] = w
	s.sess.setActiveWindow(idx)
	s.relayout()
	s.needFull = true
	s.broadcast()
	for _, warning := range warnings {
		logConfigWarning(s.configLogPath, configWarning{Reason: warning, ConfigPath: s.layoutsPath})
		if c != nil {
			c.enqueue(protocol.Notice{Text: "qmm: " + warning})
		}
	}
	return nil
}
