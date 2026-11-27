// Package sessions resolves qmm session socket paths, validates session
// names, discovers live session daemons, and resolves the on-disk executable
// path for launching fresh sessions after self-updates.
package sessions

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// DefaultName is the session name used when no explicit session is requested.
const DefaultName = "default"

const (
	maxNameLen         = 64
	defaultQueryTimout = 200 * time.Millisecond
)

// ResolveDir returns the socket directory, honoring an explicit override,
// $QMM_SOCKET_DIR, $XDG_RUNTIME_DIR/qmm-<uid>, and finally <temp>/qmm-<uid>.
func ResolveDir(override string) string {
	if override != "" {
		return override
	}
	if d := os.Getenv("QMM_SOCKET_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, fmt.Sprintf("qmm-%d", os.Getuid()))
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("qmm-%d", os.Getuid()))
}

// ValidateName checks whether name is a safe, unambiguous session identifier
// suitable for use as a Unix socket filename in the socket directory.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("session name cannot be empty")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("session name %q is too long (max %d characters)", name, maxNameLen)
	}
	if name[0] == '.' || name[0] == '-' {
		return fmt.Errorf("session name %q cannot start with %q", name, name[0])
	}
	for _, suffix := range []string{".lock", ".pid", ".log"} {
		if strings.HasSuffix(name, suffix) {
			return fmt.Errorf("session name %q cannot end with %q", name, suffix)
		}
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		ok := (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-'
		if !ok {
			return fmt.Errorf("session name %q contains invalid character %q (allowed: letters, digits, ., _, -)", name, ch)
		}
	}
	return nil
}

// SocketPath validates name (defaulting empty to DefaultName), ensures the
// socket directory exists with 0700 permissions, and returns the socket path.
func SocketPath(dirOverride, name string) (string, error) {
	if name == "" {
		name = DefaultName
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir := ResolveDir(dirOverride)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LogPath returns the log file path for a session. The default session keeps
// the historic server.log filename; named sessions use <socket>.log.
func LogPath(sock, name string) string {
	if name == "" || name == DefaultName {
		return filepath.Join(filepath.Dir(sock), "server.log")
	}
	return sock + ".log"
}

// QueryInfo dials a session socket and requests its metadata without attaching.
func QueryInfo(sock string, timeout time.Duration) (protocol.SessionInfo, error) {
	if timeout <= 0 {
		timeout = defaultQueryTimout
	}
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return protocol.SessionInfo{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)

	codec := protocol.NewCodec(conn)
	if err := codec.Send(protocol.InfoRequest{}); err != nil {
		return protocol.SessionInfo{}, err
	}
	msg, err := codec.Recv()
	if err != nil {
		return protocol.SessionInfo{}, err
	}
	info, ok := msg.(protocol.SessionInfo)
	if !ok {
		return protocol.SessionInfo{}, fmt.Errorf("unexpected info reply %T", msg)
	}
	if info.Name == "" {
		info.Name = filepath.Base(sock)
	}
	return info, nil
}

// List discovers all live sessions in the resolved socket directory. Stale or
// unresponsive sockets and auxiliary files (.lock, .pid, .log) are skipped.
// Results are sorted by creation time ascending, then by name.
func List(dirOverride string) ([]protocol.SessionInfo, error) {
	dir := ResolveDir(dirOverride)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var sockets []string
	for _, entry := range entries {
		name := entry.Name()
		if ValidateName(name) != nil {
			continue
		}
		if entry.Type()&os.ModeSocket == 0 {
			// Fall back to Stat if the filesystem reports DT_UNKNOWN.
			if entry.Type() != 0 {
				continue
			}
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil || fi.Mode()&os.ModeSocket == 0 {
				continue
			}
		}
		sockets = append(sockets, filepath.Join(dir, name))
	}
	if len(sockets) == 0 {
		return nil, nil
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		list []protocol.SessionInfo
	)
	for _, sock := range sockets {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			info, err := QueryInfo(path, defaultQueryTimout)
			if err != nil {
				return
			}
			mu.Lock()
			list = append(list, info)
			mu.Unlock()
		}(sock)
	}
	wg.Wait()

	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt != list[j].CreatedAt {
			return list[i].CreatedAt < list[j].CreatedAt
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

// NextName picks the next auto-generated session name that does not collide
// with any existing session: "default" first, then "1", "2", "3", ...
func NextName(existing []protocol.SessionInfo) string {
	used := make(map[string]struct{}, len(existing))
	for _, s := range existing {
		used[s.Name] = struct{}{}
	}
	if _, ok := used[DefaultName]; !ok {
		return DefaultName
	}
	for n := 1; ; n++ {
		candidate := strconv.Itoa(n)
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
}

// ResolveExecutable returns the clean path to the on-disk qmm binary so newly
// launched sessions and re-exec'd clients pick up any binary installed by
// `qmm update`.
//
// On Linux, atomically replacing a running executable appends " (deleted)" to
// /proc/self/exe. Pass the path captured at startup (when available); this
// helper also strips any " (deleted)" suffix and falls back to os.Executable
// and PATH lookup.
func ResolveExecutable(startupPath string) (string, error) {
	candidates := make([]string, 0, 3)
	if startupPath != "" {
		candidates = append(candidates, cleanDeletedSuffix(startupPath))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		candidates = append(candidates, cleanDeletedSuffix(exe))
	}
	if len(os.Args) > 0 && os.Args[0] != "" {
		if looked, err := exec.LookPath(os.Args[0]); err == nil && looked != "" {
			if abs, err := filepath.Abs(looked); err == nil {
				candidates = append(candidates, abs)
			} else {
				candidates = append(candidates, looked)
			}
		}
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("could not locate qmm executable on disk")
}

func cleanDeletedSuffix(path string) string {
	return strings.TrimSuffix(path, " (deleted)")
}
