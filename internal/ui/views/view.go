// Package views holds the concrete table views and the interfaces the app
// uses to drive them. Views own their data and async loading; all mutations
// return tea.Cmds so nothing blocks the event loop.
package views

import tea "github.com/charmbracelet/bubbletea"

// Openable is anything that can be opened in a tmux session. An empty
// SessionPath marks the item as not openable (e.g. template/group rows in the
// tree view); the app skips it on enter.
type Openable interface {
	SessionName() string
	SessionPath() string
	Command() string
}

// View is implemented by each concrete view. The app treats views uniformly
// through this interface.
type View interface {
	Key() string  // keybind that switches to this view
	Name() string // shown in the help bar

	Headers() []string
	Ratios() []int
	Rows() [][]any
	Len() int
	Item(i int) Openable

	// Match returns the index of the first item matching query, or -1.
	Match(query string) int

	// Load initiates async data loading.
	Load() tea.Cmd

	// Update receives messages so the view can update its own data.
	// Returns true if the message caused a data change.
	Update(msg tea.Msg) bool

	// Remove starts async removal of item i. The resulting RemovedMsg carries
	// success/failure; views must not block here.
	Remove(i int) tea.Cmd

	// HandleKey lets views implement their own keybinds. A handled key may
	// change row data (e.g. tree expand/collapse), so the app rebuilds the
	// table afterwards.
	HandleKey(key string, cursorRow int) (tea.Cmd, bool)
}

// Cloner is optionally implemented by views that support cloning.
type Cloner interface {
	Clone(url string) tea.Cmd
}

// Selector is optionally implemented by views supporting row selection.
type Selector interface {
	ToggleSelect(i int)
	HasSelection() bool
	SelectionSummary() string
}

// WorkspaceCreator is optionally implemented by views that can create
// workspaces from a selection.
type WorkspaceCreator interface {
	Selector
	CreateWorkspace(name string) tea.Cmd
}

// CellEditor is optionally implemented by views that support inline editing.
// Editability is per cell (row and column) because mixed-kind views like the
// tree only allow edits on some rows.
type CellEditor interface {
	EditableCell(row, col int) bool
	CellValue(row, col int) string
	// ApplyEdit updates local state synchronously and returns a tea.Cmd that
	// persists the change, producing an EditResultMsg.
	ApplyEdit(row, col int, value string) tea.Cmd
}

// TemplateCreator is optionally implemented by views that can save the
// current selection as a named template.
type TemplateCreator interface {
	Selector
	CreateTemplate(name string) tea.Cmd
}

// Materializer is optionally implemented by views whose rows can be turned
// into a workspace. CanMaterialize reports whether a specific row supports it
// (in the tree view only template rows do); when it returns true, enter
// prompts for a workspace name instead of opening a tmux session.
type Materializer interface {
	CanMaterialize(row int) bool
	Materialize(row int, name string) tea.Cmd
}

// TemplateRunner is optionally implemented by views whose rows can be
// materialized and immediately given a prompt authored in an editor popup
// (`o` on a template row).
type TemplateRunner interface {
	RunWithPrompt(row int, name string) tea.Cmd
}

// Notifier is optionally implemented by views that queue desktop
// notifications; the app drains them after each data change.
type Notifier interface {
	DrainNotifications() []Notification
}

type Notification struct {
	Title string
	Body  string
}

// ---- shared messages ----

// RemovedMsg reports the outcome of an async Remove. Reload asks the app to
// re-Load the active view (views whose data refreshes via a file watcher
// leave it false).
type RemovedMsg struct {
	Name   string
	Err    error
	Reload bool
}

type CloneResultMsg struct {
	URL string
	Err error
}

type WorkspaceCreatedMsg struct {
	Name string
	Err  error
}

type TemplateSavedMsg struct {
	Name string
	Err  error
}

// TemplateRunMsg reports the outcome of the edit-prompt -> materialize ->
// opencode-run flow.
type TemplateRunMsg struct {
	Workspace string
	Err       error
}

type EditResultMsg struct {
	Err error
}

// SessionsFileChangedMsg is sent (via tea.Program.Send from the file watcher)
// when the opencode state file changes.
type SessionsFileChangedMsg struct{}
