package views

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/domain/git"
	"jds.net/tui/internal/domain/opencode"
	"jds.net/tui/internal/theme"
	"jds.net/tui/internal/tmux"
)

// ---- loaded messages ----

type TemplatesLoadedMsg struct{ Templates []git.Template }
type WorkspacesLoadedMsg struct{ Workspaces []git.WorkspaceInfo }
type SessionsLoadedMsg struct{ Sessions []opencode.Session }

// ---- columns ----

const (
	treeColName = iota
	treeColStatus
	treeColTitle
	treeColNote
)

// ---- nodes ----

type nodeKind int

const (
	nodeTemplate nodeKind = iota
	nodeGroup             // pseudo container for items not owned by any template/workspace
	nodeWorkspace
	nodeSession
)

// treeNode is one visible row of the flattened tree. Exactly one of
// template/ws/sess is set (none for groups).
type treeNode struct {
	kind  nodeKind
	depth int
	id    string // stable key for collapse state
	label string // groups only

	template *git.Template
	ws       *git.WorkspaceInfo
	sess     *opencode.Session
}

func (n treeNode) expandable() bool { return n.kind != nodeSession }

// TreeView is page 1: templates -> workspaces -> sessions as a collapsible
// tree. Workspaces hang off the template whose origins hash to their
// namespace; sessions hang off the workspace whose path contains their
// directory. Leftovers land under "(other ...)" groups.
type TreeView struct {
	Manager *git.Manager
	Store   *opencode.Store
	Logger  *slog.Logger

	templates  []git.Template
	workspaces []git.WorkspaceInfo
	sessions   []opencode.Session

	nodes     []treeNode
	collapsed map[string]bool // default is expanded

	lastStatus map[string]string // slug -> status, for notifications
	pending    []Notification
}

func NewTreeView(m *git.Manager, store *opencode.Store, logger *slog.Logger) *TreeView {
	return &TreeView{Manager: m,
		Store:     store,
		Logger:    logger,
		collapsed: map[string]bool{},
	}
}

func (v *TreeView) Key() string  { return "1" }
func (v *TreeView) Name() string { return "tree" }
func (v *TreeView) Headers() []string {
	return []string{"TEMPLATE / WORKSPACE / SESSION", "STATUS", "TITLE", "NOTE"}
}
func (v *TreeView) Ratios() []int { return []int{5, 2, 4, 2} }
func (v *TreeView) Len() int      { return len(v.nodes) }

// ---- tree construction ----

func (view *TreeView) rebuild() {
	view.nodes = view.nodes[:0]

	// Assign each session to the workspace containing its directory.
	sessOf := make(map[string][]int) // workspace path -> session indices
	claimed := make([]bool, len(view.sessions))
	for sessIdx := range view.sessions {
		sessionDir := view.sessions[sessIdx].Directory
		for wsIdx := range view.workspaces {
			wsPath := view.workspaces[wsIdx].Path
			if sessionDir == wsPath || strings.HasPrefix(sessionDir, wsPath+"/") {
				sessOf[wsPath] = append(sessOf[wsPath], sessIdx)
				claimed[sessIdx] = true
				break
			}
		}
	}

	// Group workspaces by namespace (first path segment of Name).
	wsByNS := make(map[string][]int)
	for i, ws := range view.workspaces {
		ns := ws.Namespace
		wsByNS[ns] = append(wsByNS[ns], i)
	}

	addWorkspace := func(wsIdx, depth int) {
		ws := &view.workspaces[wsIdx]
		id := "w:" + ws.FullName()
		view.nodes = append(view.nodes, treeNode{kind: nodeWorkspace, depth: depth, id: id, ws: ws})
		if view.collapsed[id] {
			return
		}
		for _, sessIdx := range sessOf[ws.Path] {
			view.nodes = append(view.nodes, treeNode{
				kind:  nodeSession,
				depth: depth + 1,
				id:    "s:" + view.sessions[sessIdx].ID,
				sess:  &view.sessions[sessIdx],
			})
		}
	}

	// Templates own workspaces whose namespace matches their origins hash.
	ownedWS := make(map[int]bool)
	for templIdx := range view.templates {
		templ := &view.templates[templIdx]
		id := "t:" + templ.Name
		view.nodes = append(view.nodes, treeNode{
			kind:     nodeTemplate,
			id:       id,
			template: templ,
		})
		children := wsByNS[git.NamespaceFromOrigins(templ.Origins)]
		for _, wsIdx := range children {
			ownedWS[wsIdx] = true
		}
		if view.collapsed[id] {
			continue
		}
		for _, wi := range children {
			addWorkspace(wi, 1)
		}
	}

	// Workspaces with no owning template.
	var otherWS []int
	for i := range view.workspaces {
		if !ownedWS[i] {
			otherWS = append(otherWS, i)
		}
	}
	if len(otherWS) > 0 {
		const id = "g:workspaces"
		view.nodes = append(view.nodes, treeNode{kind: nodeGroup, id: id, label: "(other workspaces)"})
		if !view.collapsed[id] {
			for _, wi := range otherWS {
				addWorkspace(wi, 1)
			}
		}
	}

	// Sessions outside every workspace.
	var orphans []int
	for i := range view.sessions {
		if !claimed[i] {
			orphans = append(orphans, i)
		}
	}
	if len(orphans) > 0 {
		const id = "g:sessions"
		view.nodes = append(view.nodes, treeNode{kind: nodeGroup, id: id, label: "(other sessions)"})
		if !view.collapsed[id] {
			for _, si := range orphans {
				view.nodes = append(view.nodes, treeNode{
					kind: nodeSession, depth: 1,
					id: "s:" + view.sessions[si].ID, sess: &view.sessions[si],
				})
			}
		}
	}
}

// ---- rendering ----

var (
	statusStyle = map[string]lipgloss.Style{
		"blocked": lipgloss.NewStyle().Foreground(theme.Red).Bold(true),
		"idle":    lipgloss.NewStyle().Foreground(theme.Yellow),
		"working": lipgloss.NewStyle().Foreground(theme.Green),
	}
	dirtyStyle = lipgloss.NewStyle().Foreground(theme.Red)
	mutedStyle = lipgloss.NewStyle().Foreground(theme.Overlay0)
)

func styleStatus(s string) string {
	if st, ok := statusStyle[s]; ok {
		return st.Render(s)
	}
	return s
}

func shortOrigin(origin string) string {
	return strings.TrimSuffix(filepath.Base(origin), ".git")
}

func (v *TreeView) marker(n treeNode) string {
	if !n.expandable() {
		return "  "
	}
	if v.collapsed[n.id] {
		return "▸ "
	}
	return "▾ "
}

func (view *TreeView) Rows() [][]any {
	rows := make([][]any, len(view.nodes))
	for idx, node := range view.nodes {
		name := strings.Repeat("   ", node.depth) + view.marker(node)
		switch node.kind {
		case nodeTemplate:
			names := make([]string, len(node.template.Origins))
			for j, origin := range node.template.Origins {
				names[j] = shortOrigin(origin)
			}
			rows[idx] = []any{
				name + node.template.Name,
				"",
				"",
				"",
			}
		case nodeGroup:
			rows[idx] = []any{name + mutedStyle.Render(node.label), "", "", ""}
		case nodeWorkspace:
			status := "clean"
			if node.ws.Dirty {
				status = dirtyStyle.Render("dirty")
			}
			rows[idx] = []any{
				name + node.ws.Name,
				status,
				"",
				"",
			}
		case nodeSession:
			age := relAge(node.sess.UpdatedAt)
			if age != "" {
				rows[idx] = []any{
					name + node.sess.Slug,
					styleStatus(node.sess.Status),
					node.sess.DisplayTitle(),
					relAge(node.sess.UpdatedAt) + " ago",
				}
			} else {
				rows[idx] = []any{
					name + node.sess.Slug,
					styleStatus(node.sess.Status),
					node.sess.DisplayTitle(),
					"",
				}
			}
		}
	}
	return rows
}

func relAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---- Openable ----

type treeItem struct{ n treeNode }

func (t treeItem) SessionName() string {
	switch t.n.kind {
	case nodeTemplate:
		return t.n.template.Name
	case nodeWorkspace:
		return t.n.ws.FullName()
	case nodeSession:
		return t.n.sess.Slug
	}
	return ""
}

// SessionPath is empty for template/group rows, marking them not openable.
func (t treeItem) SessionPath() string {
	switch t.n.kind {
	case nodeWorkspace:
		return t.n.ws.Path
	case nodeSession:
		return t.n.sess.Directory
	}
	return ""
}

func (t treeItem) Command() string {
	if t.n.kind == nodeSession {
		return fmt.Sprintf("%s/.local/bin/oc -s %s", os.Getenv("HOME"), t.n.sess.ID)
	}
	return ""
}

func (v *TreeView) Item(i int) Openable { return treeItem{v.nodes[i]} }

// ---- search ----

func (v *TreeView) Match(query string) int {
	for i, n := range v.nodes {
		var hay []string
		switch n.kind {
		case nodeTemplate:
			hay = append([]string{n.template.Name}, n.template.Origins...)
		case nodeGroup:
			hay = []string{n.label}
		case nodeWorkspace:
			hay = []string{n.ws.Name, n.ws.Path}
		case nodeSession:
			hay = []string{n.sess.Slug, n.sess.ID, n.sess.DisplayTitle(), n.sess.Directory}
		}
		for _, h := range hay {
			if strings.Contains(strings.ToLower(h), query) {
				return i
			}
		}
	}
	return -1
}

// ---- loading ----

func (v *TreeView) Load() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			ts, err := v.Manager.ReadTemplates()
			if err != nil {
				v.Manager.Logger.Error("reading templates", "err", err)
				return TemplatesLoadedMsg{}
			}
			slices.SortFunc(ts, func(a, b git.Template) int {
				return strings.Compare(a.Name, b.Name)
			})
			return TemplatesLoadedMsg{Templates: ts}
		},
		func() tea.Msg {
			ws, err := v.Manager.FindWorkspaces()
			if err != nil {
				return WorkspacesLoadedMsg{}
			}
			slices.SortFunc(ws, func(a, b git.WorkspaceInfo) int {
				return strings.Compare(a.FullName(), b.FullName())
			})
			return WorkspacesLoadedMsg{Workspaces: ws}
		},
		func() tea.Msg {
			sessions, err := v.Store.Load()
			if err != nil {
				v.Logger.Error("loading opencode sessions", slog.String("err", err.Error()))
				return SessionsLoadedMsg{}
			}
			slices.SortFunc(sessions, func(a, b opencode.Session) int {
				return -a.UpdatedAt.Compare(b.UpdatedAt)
			})
			return SessionsLoadedMsg{Sessions: sessions}
		},
	)
}

func (v *TreeView) Update(msg tea.Msg) bool {
	switch m := msg.(type) {
	case TemplatesLoadedMsg:
		v.templates = m.Templates
	case WorkspacesLoadedMsg:
		v.workspaces = m.Workspaces
	case SessionsLoadedMsg:
		// Queue notifications for status transitions into blocked/idle.
		for _, s := range m.Sessions {
			prev, existed := v.lastStatus[s.Slug]
			if existed && prev != s.Status && (s.Status == "blocked" || s.Status == "idle") {
				v.pending = append(v.pending, Notification{
					Title: fmt.Sprintf("[%s] is %s", s.Slug, s.Status),
					Body:  s.DisplayTitle(),
				})
			}
		}
		v.sessions = m.Sessions
		v.lastStatus = make(map[string]string, len(v.sessions))
		for _, s := range v.sessions {
			v.lastStatus[s.Slug] = s.Status
		}
	default:
		return false
	}
	v.rebuild()
	return true
}

func (v *TreeView) DrainNotifications() []Notification {
	p := v.pending
	v.pending = nil
	return p
}

// ---- keybinds: r expand, m collapse, o popups ----

func (v *TreeView) HandleKey(key string, cursorRow int) (tea.Cmd, bool) {
	if cursorRow < 0 || cursorRow >= len(v.nodes) {
		return nil, false
	}
	n := v.nodes[cursorRow]
	switch key {
	case "r":
		if n.expandable() && v.collapsed[n.id] {
			delete(v.collapsed, n.id)
			v.rebuild()
		}
		return nil, true
	case "m":
		if n.expandable() && !v.collapsed[n.id] {
			v.collapsed[n.id] = true
			v.rebuild()
		}
		return nil, true
	case "t":
		switch n.kind {
		case nodeWorkspace:
			if err := tmux.Popup(n.ws.Path, "nvim +ter"); err != nil {
				v.Logger.Error("creating tmux popup", slog.String("error", err.Error()))
			}
			return nil, true
		case nodeSession:
			if err := tmux.Popup(n.sess.Directory, "nvim +ter"); err != nil {
				v.Logger.Error("creating tmux popup", slog.String("error", err.Error()))
			}
			return nil, true
		}
	case "o":
		switch n.kind {
		case nodeWorkspace:
			if err := tmux.Popup(n.ws.Path, os.Getenv("HOME")+"/.local/bin/oc"); err != nil {
				v.Logger.Error("creating tmux popup", slog.String("error", err.Error()))
			}
			return nil, true
		case nodeSession:
			cmd := fmt.Sprintf("%s/.local/bin/oc -s %s", os.Getenv("HOME"), n.sess.ID)
			if err := tmux.Popup(n.sess.Directory, cmd); err != nil {
				v.Logger.Error("creating tmux popup", slog.String("error", err.Error()))
			}
			return nil, true
		}
		// Template rows fall through so the app runs the TemplateRunner
		// prompt-in-editor flow.
	}
	return nil, false
}

// ---- removal (per node kind) ----

func (appView *TreeView) Remove(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(appView.nodes) {
		return nil
	}
	node := appView.nodes[rowIdx]
	switch node.kind {
	case nodeTemplate:
		name := node.template.Name
		return func() tea.Msg {
			return RemovedMsg{
				Name:   name,
				Err:    appView.Manager.DeleteTemplate(name),
				Reload: true,
			}
		}
	case nodeWorkspace:
		ws := *node.ws
		return func() tea.Msg {
			// Kill the tmux session first so open file handles don't block cleanup.
			exec.Command("tmux", "kill-session", "-t", ws.FullName()).Run()
			return RemovedMsg{
				Name:   ws.Name,
				Err:    appView.Manager.RemoveWorkspace(ws),
				Reload: true,
			}
		}
	case nodeSession:
		// Data refreshes via the state-file watcher once the plugin processes
		// session.deleted, so Reload stays false.
		sess := *node.sess
		return func() tea.Msg {
			return RemovedMsg{
				Name: sess.Slug,
				Err:  appView.Store.Delete(sess.ID),
			}
		}
	}
	return nil
}

// ---- Materializer (enter on a template row) ----

func (v *TreeView) CanMaterialize(row int) bool {
	return row >= 0 && row < len(v.nodes) && v.nodes[row].kind == nodeTemplate
}

func (v *TreeView) Materialize(row int, name string) tea.Cmd {
	if !v.CanMaterialize(row) {
		return nil
	}
	t := *v.nodes[row].template
	return func() tea.Msg {
		repos, err := v.Manager.ResolveTemplate(t)
		if err != nil {
			return WorkspaceCreatedMsg{Name: name, Err: err}
		}
		_, err = v.Manager.CreateWorkspace(git.Namespace(repos), name, repos)
		return WorkspaceCreatedMsg{Name: name, Err: err}
	}
}

// ---- TemplateRunner (`o` on a template row) ----

// RunWithPrompt opens nvim in a tmux popup on a fresh markdown file, then
// (once the editor exits) materializes the template into a workspace named
// name and dispatches the file's contents to opencode as the prompt for a new
// session in that workspace.
func (v *TreeView) RunWithPrompt(row int, name string) tea.Cmd {
	if !v.CanMaterialize(row) {
		return nil
	}
	t := *v.nodes[row].template
	return func() tea.Msg {
		file := fmt.Sprintf("/tmp/devprompt-%s.md", time.Now().Format("20060102-150405"))
		if err := tmux.Popup("/tmp", "nvim "+file); err != nil {
			return TemplateRunMsg{Workspace: name, Err: fmt.Errorf("editor popup: %w", err)}
		}

		prompt, err := os.ReadFile(file)
		if err != nil || len(bytes.TrimSpace(prompt)) == 0 {
			return TemplateRunMsg{Workspace: name,
				Err: fmt.Errorf("prompt file %s is empty; aborted before materializing", file)}
		}

		repos, err := v.Manager.ResolveTemplate(t)
		if err != nil {
			return TemplateRunMsg{Workspace: name, Err: err}
		}
		wsPath, err := v.Manager.CreateWorkspace(git.Namespace(repos), name, repos)
		if err != nil {
			return TemplateRunMsg{Workspace: name, Err: err}
		}

		sessionID, err := v.Store.CreateSession(wsPath)
		if err != nil {
			return TemplateRunMsg{Workspace: name, Err: err}
		}
		if err := v.Store.PromptAsync(sessionID, wsPath, string(prompt)); err != nil {
			return TemplateRunMsg{Workspace: name, Err: err}
		}

		v.Manager.Logger.Info("created oc session", slog.String("sid", sessionID))
		return TemplateRunMsg{Workspace: name}
	}
}

// ---- CellEditor (session title/note) ----

func (v *TreeView) EditableCell(row, col int) bool {
	return row >= 0 && row < len(v.nodes) && v.nodes[row].kind == nodeSession &&
		(col == treeColTitle || col == treeColNote)
}

func (v *TreeView) CellValue(row, col int) string {
	if !v.EditableCell(row, col) {
		return ""
	}
	s := v.nodes[row].sess
	if col == treeColTitle {
		return s.DisplayTitle()
	}
	return s.Note
}

func (v *TreeView) ApplyEdit(row, col int, value string) tea.Cmd {
	if !v.EditableCell(row, col) {
		return func() tea.Msg { return EditResultMsg{Err: fmt.Errorf("cell %d,%d is not editable", row, col)} }
	}
	s := v.nodes[row].sess
	if col == treeColTitle {
		s.UserTitle = value
	} else {
		s.Note = value
	}
	id, title, note := s.ID, s.UserTitle, s.Note
	return func() tea.Msg {
		return EditResultMsg{Err: v.Store.SetUsermeta(id, title, note)}
	}
}

var (
	_ View           = (*TreeView)(nil)
	_ Materializer   = (*TreeView)(nil)
	_ TemplateRunner = (*TreeView)(nil)
	_ CellEditor     = (*TreeView)(nil)
	_ Notifier       = (*TreeView)(nil)
)
