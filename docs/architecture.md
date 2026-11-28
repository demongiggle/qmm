# Server architecture

One daemon owns a session. Its event loop in `internal/server/server.go`
serializes changes to windows, focus, settings and attached clients. Each pane
has a PTY reader. Each attachment has a `clientView` for its menus, selection,
drag preview and previous frame, plus a writer for its outgoing queue.

The paths below are relative to `internal/server` unless otherwise indicated.

| Flow | Entry points and state owners | Representative tests |
| --- | --- | --- |
| Attach and disconnect | `cmd/qmm/main.go` selects the session. `client.AttachWithSwitch` in `internal/client/client.go` starts the handshake. In `server.go`, `acceptLoop` reads the first message and `registerClient` adds the connection to the event loop. | `integration_test.go` · `TestServerLifecycle` |
| Keys, pointer actions and menus | `handleClientMessage` in `actions.go` receives the decoded message. `routeOverlay` in `interaction.go` decides who consumes a key or action. `menuView` in `client_view.go` owns the active full-screen menu. Add bindings to `prefixBindings` in `actions.go` and put menu-specific actions in the feature file. | `interaction_behavior_test.go` · `TestOverlayRoutesMouseAndKeyboardPerClient` |
| Divider drag, zoom and resize | `dragUpdate` in `drag.go` edits a clone for the initiating client. `dragEnd` installs it in the shared window. In `pane_geometry.go`, `frameGeometry` selects the tree to draw, `paneRects` supplies committed hit and copy targets, and `relayout` sizes the terminals. | `drag_test.go` · `TestSplitterDragCommitsPTYSizeOnRelease`. `geometry_behavior_test.go` · `TestNestedZoomUsesSmallestClientDimensionsAndRestoresSplit` |
| PTY output, history and copy | `pane.readLoop` and `copyBufferAtCursor` in `pane_output.go` feed the PTY into the VT and snapshot the copy buffer, respectively. `State.scrollUp` in `internal/vt10x/state.go` passes outgoing rows to `scrollback.AppendRow` in `scrollback.go`. `projectVTRow` in `terminal_cells.go` supplies terminal-column cells to both frames and copy snapshots. | `terminal_behavior_test.go` · `TestPaneHistoryFirstScrollAfterShrink`, `TestPaneHistoryBoldColorAcrossReads` |
| Frame delivery | `broadcast` in `server.go` composes each client's view. `clientView.compose` in `compose.go` diffs against that client's previous grid. `enqueueFrame` in `server.go` coalesces pending deltas. `attach.applyFrame` in `internal/client/render.go` updates the local terminal. | `connection_test.go` · `TestConnectionSlowClientKeepsDeltasAndDoesNotBlockPeer` |
| Settings and saved layouts | `loadConfig` in `config.go` populates session-wide settings. `restoreLayout` in `layouts.go` creates a window from a saved tree. The settings and layouts menus hold each client's cursor and editing state. | `persistence_behavior_test.go` · `TestExistingConfigControlsRenderedAndInteractiveBehavior`, `TestSavedLayoutV1RestoresThroughMenu` |

## Where history comes from

The VT parses PTY output and owns the live screen and cursor. When a
main-screen scroll region starts at row zero, the VT passes the outgoing
rows to the pane before moving or clearing any cells. Deleting lines,
scrolling inside a region below row zero, and scrolling the alternate screen
leave history intact. CSI 3 J on the main screen clears history.

Each pane keeps the latest 2,000 outgoing rows in a ring. It uses the same
cell projection as live frames to preserve colors, styles and wide-character
columns. Copy mode snapshots history, the live screen and the cursor under
the VT lock. Its saved rows remain valid when later output advances the ring.
