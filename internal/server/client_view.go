package server

import (
	"time"

	"github.com/demongiggle/qmm/internal/protocol"
)

// clientView is owned by one attachment on the server event loop. Input
// handlers and frame composition use the same menu cursors and selections.
type clientView struct {
	menuView
	frames    frameDiff
	forceFull bool

	// Border-drag state (client-initiated).
	drag *splitterDrag

	// Shutdown confirmation is local to the client that pressed prefix+&.
	confirmOpen bool

	// prefix+[ opens copy mode: a per-client, vim-keyed view of the active
	// pane's scrollback with an optional selection.
	copyMode *copyModeState

	// selection is a mouse drag selection. It is not copy mode: it never
	// captures the keyboard and never blocks other mouse actions.
	selection *selectionState

	// Config recovery warnings are local. A startup warning is shown once to
	// the first attached client; runtime failures target their triggering
	// client.
	configWarning     configWarning
	configWarningOpen bool

	// prefix+q shows transient pane numbers on one client. The input buffer
	// permits two-digit pane numbers when a window has more than ten panes.
	displayPanesOpen     bool
	displayPanesInput    string
	displayPanesDeadline time.Time

	// prefix+0..9 jumps to a numbered window. The input buffer stays open
	// while the number is a prefix of a longer existing window index, so
	// prefix+1 then 0 reaches window 10.
	windowGotoOpen     bool
	windowGotoInput    string
	windowGotoDeadline time.Time
}

type menuKind uint8

const (
	menuNone menuKind = iota
	menuHelp
	menuSettings
	menuSessions
	menuLayouts
)

// These full-screen menus replace each other. Dialogs, copy mode and the
// timed pane/window pickers retain their independent lifetimes.
type menuView struct {
	menu    menuKind
	helpTop int

	// prefix+s opens a categorized settings page. Option values are shared,
	// but the cursor and scroll position are local to this client.
	settingsCursor int
	settingsTop    int

	// prefix+S opens a session switcher/creator overlay local to this client.
	sessionsLoading bool
	sessionsList    []protocol.SessionInfo
	sessionsCursor  int
	sessionsTop     int
	sessionsNaming  bool
	sessionsInput   string
	sessionsError   string

	// prefix+L opens a saved-layout menu local to this client. The layouts
	// themselves are shared and persisted; only the view state is per-client.
	layoutsList             []savedLayout
	layoutsCursor           int
	layoutsTop              int
	layoutsNaming           bool
	layoutsInput            string
	layoutsError            string
	layoutsMessage          string
	layoutsConfirmOverwrite string
	layoutsConfirmDelete    string
}

// setMenu starts a fresh menu and releases the previous list and edit state.
func (v *clientView) setMenu(menu menuKind) {
	v.menuView = menuView{menu: menu}
}
