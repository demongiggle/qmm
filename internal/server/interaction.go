package server

import "github.com/demongiggle/qmm/internal/protocol"

// cancelInterruptedGestures runs even for resize, detach and shutdown.
func (s *Server) cancelInterruptedGestures(c *clientConn, msg any) {
	s.cancelInterruptedDrag(c, msg)
	// A mouse selection is not modal: only the messages that extend it (drag
	// updates, wheel steps, and the input that may carry the copy key) keep it
	// alive. Anything else — focus, window switch, split, resize, detach —
	// cancels the selection first and then runs normally.
	if c.selection != nil && !selectionKeepsAlive(msg) {
		s.clearSelection(c)
	}
}

func (s *Server) handleInput(c *clientConn, data []byte) {
	// A pending mouse selection intercepts exactly one key: the configured
	// copy key. Every other key returns to the live pane first, so typing,
	// prefix chords and detach all keep working while a selection is visible.
	if c.selection != nil {
		if s.sess != nil && string(data) == copyKeySequence(s.sess.opts()) {
			// The copy key is a keystroke of its own: it consumes a pending
			// prefix instead of leaving the next key to continue the chord.
			c.prefixArmed = false
			c.prefixBuf = nil
			c.clearRepeat()
			s.copyMouseSelection(c)
			return
		}
		s.clearSelection(c)
	}
	if !s.routeOverlay(c, protocol.Input{Data: data}) {
		s.handlePaneInput(c, data)
	}
}

// routeOverlay applies input precedence for one attachment. A handled action
// is consumed even if it dismisses a menu. Lifecycle messages bypass this path.
func (s *Server) routeOverlay(c *clientConn, msg any) bool {
	input, keyboard := msg.(protocol.Input)
	route := func(key func(*clientConn, []byte), dismiss func(*clientConn)) bool {
		if keyboard {
			c.clearRepeat()
			key(c, input.Data)
		} else if dismiss != nil {
			dismiss(c)
		}
		return true
	}

	// Copy mode owns its pane's keys; a recovery warning blocks all gestures,
	// including a click that would otherwise leave copy mode.
	if !keyboard && c.configWarningOpen {
		return true
	}
	if st := c.copyMode; st != nil && st.open {
		if keyboard {
			return route(s.handleCopyModeInput, nil)
		}
		if s.copyModeConsumesAction(c, msg) {
			return true
		}
	}
	if c.configWarningOpen {
		return route(s.handleConfigWarningInput, nil)
	}
	if c.confirmOpen {
		return route(s.handleShutdownConfirmInput, nil)
	}
	_, sessionRefresh := msg.(sessionListResult)
	if c.sessionsOpen && !sessionRefresh {
		return route(s.handleSessionsMenuInput, s.closeSessionsMenu)
	}
	if c.layoutsOpen {
		return route(s.handleLayoutsMenuInput, s.closeLayoutsMenu)
	}
	if c.settingsOpen {
		return route(s.handleSettingsInput, s.closeSettings)
	}
	if keyboard && s.displayPanesExpired(c) {
		s.expireDisplayPanesFor(c)
	}
	if c.displayPanesOpen {
		return route(s.handleDisplayPanesInput, s.closeDisplayPanes)
	}
	// Numeric window jumps consume individual digits in handlePaneInput.
	if !keyboard && c.windowGotoOpen {
		s.closeWindowGoto(c)
		return true
	}
	if c.helpOpen {
		return route(s.handleHelpInput, s.closeHelp)
	}
	return false
}
