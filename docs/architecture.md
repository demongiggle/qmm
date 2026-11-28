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
| PTY output, history and copy | `pane.readLoop` and `copyBufferAtCursor` in `pane_output.go` connect the live VT to the history recorder. `scrollback.scrollUp` in `scrollback.go` retains displaced rows. `projectVTRow` in `terminal_cells.go` supplies terminal-column cells to both frames and copy snapshots. | `terminal_behavior_test.go` · `TestPaneOutputCompletesSplitCSIBeforeRenderingAndRecording` |
| Frame delivery | `broadcast` in `server.go` composes each client's view. `clientView.compose` in `compose.go` diffs against that client's previous grid. `enqueueFrame` in `server.go` coalesces pending deltas. `attach.applyFrame` in `internal/client/render.go` updates the local terminal. | `connection_test.go` · `TestConnectionSlowClientKeepsDeltasAndDoesNotBlockPeer` |
| Settings and saved layouts | `loadConfig` in `config.go` populates session-wide settings. `restoreLayout` in `layouts.go` creates a window from a saved tree. The settings and layouts menus hold each client's cursor and editing state. | `persistence_behavior_test.go` · `TestExistingConfigControlsRenderedAndInteractiveBehavior`, `TestSavedLayoutV1RestoresThroughMenu` |

## Where history comes from

The VT owns the live screen and cursor. The recorder reads the same PTY bytes
and appends rows scrolled off the top of the main screen. After each
successful VT parse, the reader refreshes the recorder's screen from the VT.
Copy mode joins recorded history with a snapshot of the live screen.

A future VT history hook could capture outgoing rows in `vt10x.State.scrollUp`
before it clears them. It would need to distinguish main-screen output, scroll
regions and line deletion. It would also change existing results when the VT
and recorder disagree after a height reduction or on bold base-palette
colors across PTY reads. Those cases need explicit expected behavior before
switching the history source.
