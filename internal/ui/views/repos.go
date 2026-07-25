package views

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"jds.net/tui/internal/domain/git"
)

type ReposLoadedMsg struct {
	Repos []git.Repo
}

type RepoView struct {
	Manager  *git.Manager
	Data     []git.Repo
	Selected map[int]bool
}

func NewRepoView(m *git.Manager) *RepoView {
	rv := &RepoView{Manager: m, Selected: map[int]bool{}}
	if data, err := m.ReadCache(); err == nil {
		rv.Data = data
	}
	return rv
}

type repoItem struct{ git.Repo }

func (r repoItem) SessionName() string { return r.Name }
func (r repoItem) SessionPath() string { return r.Path }
func (r repoItem) Command() string     { return "" }

func (v *RepoView) Key() string         { return "2" }
func (v *RepoView) Name() string        { return "repos" }
func (v *RepoView) Ratios() []int       { return []int{1, 4, 1, 1, 6} }
func (v *RepoView) Headers() []string   { return []string{"", "Name", "Bare", "Dirty", "Origin"} }
func (v *RepoView) Len() int            { return len(v.Data) }
func (v *RepoView) Item(i int) Openable { return repoItem{v.Data[i]} }

func (v *RepoView) HandleKey(key string, cursorRow int) (tea.Cmd, bool) { return nil, false }

func (v *RepoView) Rows() [][]any {
	rows := make([][]any, len(v.Data))
	for i, r := range v.Data {
		check := "[ ]"
		if v.Selected[i] {
			check = "[x]"
		}
		rows[i] = []any{check, r.Name, yesNo(r.Bare), yesNo(r.Dirty), r.Origin}
	}
	return rows
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (v *RepoView) Match(query string) int {
	for i, r := range v.Data {
		if strings.Contains(strings.ToLower(r.Name), query) ||
			strings.Contains(strings.ToLower(r.Path), query) {
			return i
		}
	}
	return -1
}

func (v *RepoView) Load() tea.Cmd {
	return func() tea.Msg {
		repos, err := v.Manager.FindGitReposIn(v.Manager.BaseDir + "/projects")
		if err != nil {
			return ReposLoadedMsg{}
		}
		repos = slices.DeleteFunc(repos, func(r git.Repo) bool { return r.Origin == "" })
		v.Manager.WriteCache(repos)
		return ReposLoadedMsg{Repos: repos}
	}
}

func (v *RepoView) Update(msg tea.Msg) bool {
	if m, ok := msg.(ReposLoadedMsg); ok {
		v.Data = m.Repos
		v.Selected = map[int]bool{}
		return true
	}
	return false
}

// Remove drops the repo from the in-memory list (the repo stays on disk; a
// rescan will re-add it, which is why Reload is false).
func (v *RepoView) Remove(i int) tea.Cmd {
	if i < 0 || i >= len(v.Data) {
		return nil
	}
	name := v.Data[i].Name
	v.Data = append(v.Data[:i], v.Data[i+1:]...)
	delete(v.Selected, i)
	return func() tea.Msg { return RemovedMsg{Name: name} }
}

// ---- Selector / WorkspaceCreator ----

func (v *RepoView) ToggleSelect(i int) {
	if i < 0 || i >= len(v.Data) {
		return
	}
	v.Selected[i] = !v.Selected[i]
}

func (v *RepoView) HasSelection() bool {
	for _, s := range v.Selected {
		if s {
			return true
		}
	}
	return false
}

func (v *RepoView) selectedRepos() []git.Repo {
	var out []git.Repo
	for i, r := range v.Data {
		if v.Selected[i] {
			out = append(out, r)
		}
	}
	return out
}

func (v *RepoView) SelectionSummary() string {
	repos := v.selectedRepos()
	if len(repos) == 0 {
		return "no repos selected"
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return fmt.Sprintf("%d selected: %s", len(repos), strings.Join(names, ", "))
}

func (v *RepoView) Clone(url string) tea.Cmd {
	return func() tea.Msg {
		return CloneResultMsg{URL: url, Err: v.Manager.CloneRepo(url)}
	}
}

// CreateTemplate saves the current selection as a named template, keyed by
// origin URLs so it survives rescans and path moves.
func (v *RepoView) CreateTemplate(name string) tea.Cmd {
	repos := v.selectedRepos()
	origins := make([]string, len(repos))
	for i, r := range repos {
		origins[i] = r.Origin
	}
	return func() tea.Msg {
		err := v.Manager.SaveTemplate(git.Template{Name: name, Origins: origins})
		return TemplateSavedMsg{Name: name, Err: err}
	}
}

func (v *RepoView) CreateWorkspace(name string) tea.Cmd {
	repos := v.selectedRepos()
	return func() tea.Msg {
		_, err := v.Manager.CreateWorkspace(git.Namespace(repos), name, repos)
		return WorkspaceCreatedMsg{Name: name, Err: err}
	}
}

var (
	_ View             = (*RepoView)(nil)
	_ Cloner           = (*RepoView)(nil)
	_ WorkspaceCreator = (*RepoView)(nil)
	_ TemplateCreator  = (*RepoView)(nil)
)
