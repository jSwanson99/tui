package ui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/theme"
	"jds.net/tui/internal/tmux"
	"jds.net/tui/internal/ui/views"
)

// App is the root bubbletea model. It owns routing between views, the table,
// the status bar, the prompt, and the error overlay. Every mutation that can
// block (git, HTTP, tmux popups) happens inside a tea.Cmd; Update never
// performs I/O beyond firing tmux attach on enter.
type App struct {
	width, height int

	views     []views.View
	active    int
	keyToView map[string]int

	tbl    *Table
	status *StatusBar
	prompt *Prompt
	errMsg string

	pendingG bool
	logger   *slog.Logger
}

func NewApp(views []views.View, logger *slog.Logger) *App {
	if len(views) == 0 {
		panic("at least one view is required")
	}

	keyToView := make(map[string]int, len(views))
	for i, v := range views {
		keyToView[v.Key()] = i
	}

	app := &App{
		views:     views,
		keyToView: keyToView,
		logger:    logger,
		status:    NewStatusBar(),
		tbl:       NewTable(views[0].Headers(), views[0].Ratios(), logger),
	}
	app.status.SetHelp(helpText(views, 0))
	return app
}

func (a *App) view() views.View { return a.views[a.active] }

func (a *App) Init() tea.Cmd { return a.view().Load() }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Offer every message to the active view first; rebuild on data change.
	if a.view().Update(msg) {
		a.tbl.SetRows(a.view().Rows())
		a.tbl.Clamp(a.view().Len())
		a.status.SetAction(fmt.Sprintf("Loaded %d items", a.view().Len()))
		if n, ok := a.view().(views.Notifier); ok {
			for _, note := range n.DrainNotifications() {
				Notify(note.Title, note.Body)
			}
		}
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.tbl.SetSize(msg.Width, msg.Height-1)
		a.status.SetWidth(msg.Width)
		return a, nil

	case views.SessionsFileChangedMsg:
		return a, a.view().Load()

	case views.RemovedMsg:
		if msg.Err != nil {
			a.errMsg = msg.Err.Error()
		} else {
			a.status.SetAction(fmt.Sprintf("Removed %q", msg.Name))
		}
		a.tbl.SetRows(a.view().Rows())
		a.tbl.Clamp(a.view().Len())
		if msg.Reload {
			return a, a.view().Load()
		}
		return a, nil

	case views.CloneResultMsg:
		if msg.Err != nil {
			a.errMsg = fmt.Sprintf("Clone failed: %v", msg.Err)
		} else {
			a.status.SetAction("Clone succeeded")
		}
		return a, a.view().Load()

	case views.WorkspaceCreatedMsg:
		if msg.Err != nil {
			a.errMsg = fmt.Sprintf("Workspace failed: %v", msg.Err)
		} else {
			a.status.SetAction(fmt.Sprintf("Workspace %q created", msg.Name))
		}
		return a, a.view().Load()

	case views.TemplateSavedMsg:
		if msg.Err != nil {
			a.errMsg = fmt.Sprintf("Template failed: %v", msg.Err)
		} else {
			a.status.SetAction(fmt.Sprintf("Template %q saved", msg.Name))
		}
		return a, a.view().Load()

	case views.TemplateRunMsg:
		if msg.Err != nil {
			a.errMsg = fmt.Sprintf("Template run failed: %v", msg.Err)
		} else {
			a.status.SetAction(fmt.Sprintf("Workspace %q created, prompt dispatched", msg.Workspace))
		}
		return a, a.view().Load()

	case views.EditResultMsg:
		if msg.Err != nil {
			a.errMsg = fmt.Sprintf("Edit failed: %v", msg.Err)
		} else {
			a.tbl.SetRows(a.view().Rows())
			a.status.SetAction("Saved")
		}
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Error overlay eats keys until dismissed.
	if a.errMsg != "" {
		if key == "esc" || key == "q" {
			a.errMsg = ""
		}
		return a, nil
	}

	if a.prompt != nil {
		return a.handlePromptKey(msg)
	}

	// View switching.
	if idx, ok := a.keyToView[key]; ok && idx != a.active {
		a.switchView(idx)
		return a, a.view().Load()
	}

	// gg -> top.
	if a.pendingG {
		a.pendingG = false
		if key == "g" {
			a.tbl.SetCursor(0)
			return a, nil
		}
	}

	// View-specific keybinds take precedence over the defaults below. A
	// handled key may have changed row data (tree expand/collapse), so the
	// table is rebuilt.
	if cmd, handled := a.view().HandleKey(key, a.tbl.CursorRow()); handled {
		a.tbl.SetRows(a.view().Rows())
		a.tbl.Clamp(a.view().Len())
		return a, cmd
	}

	switch key {
	case "q", "ctrl+c":
		return a, tea.Quit

	case "g":
		a.pendingG = true

	case "G":
		a.tbl.SetCursor(a.view().Len() - 1)

	case "j":
		a.tbl.Down()
	case "k":
		a.tbl.Up()
	case "h":
		a.tbl.Left()
	case "l":
		a.tbl.Right()

	case "/":
		a.prompt = NewPrompt(PromptSearch, "/", "", "")

	case "c":
		if _, ok := a.view().(views.Cloner); ok {
			a.prompt = NewPrompt(PromptClone, "clone: ", "", "git clone URL")
		}

	case " ":
		if sel, ok := a.view().(views.Selector); ok {
			sel.ToggleSelect(a.tbl.CursorRow())
			a.tbl.SetRows(a.view().Rows())
			a.status.SetAction(sel.SelectionSummary())
		}

	case "w":
		if wc, ok := a.view().(views.WorkspaceCreator); ok {
			if !wc.HasSelection() {
				a.status.SetAction("select repos with <space> first")
			} else {
				a.prompt = NewPrompt(PromptWorkspace, "workspace name: ", "", "workspace name")
			}
		}

	case "o":
		// Only reached when the active view didn't claim "o" in HandleKey
		// (the tree claims it on workspace/session rows for popups); on a
		// template row this starts the materialize + editor-prompt flow.
		if _, ok := a.view().(views.TemplateRunner); ok {
			row := a.tbl.CursorRow()
			if row < 0 || row >= a.view().Len() {
				break
			}
			// Guard: only rows that can materialize can run.
			if mv, ok := a.view().(views.Materializer); ok && !mv.CanMaterialize(row) {
				break
			}
			p := NewPrompt(PromptRunTemplate, "workspace name: ", "", "workspace name")
			p.Row = row
			a.prompt = p
		}

	case "t":
		if tc, ok := a.view().(views.TemplateCreator); ok {
			if !tc.HasSelection() {
				a.status.SetAction("select repos with <space> first")
			} else {
				a.prompt = NewPrompt(PromptTemplate, "template name: ", "", "template name")
			}
		}

	case "e":
		ce, ok := a.view().(views.CellEditor)
		if !ok {
			a.status.SetAction("this view doesn't support editing")
			break
		}
		col, row := a.tbl.CursorPos()
		if row < 0 || row >= a.view().Len() {
			break
		}
		if !ce.EditableCell(row, col) {
			a.status.SetAction("this cell is not editable")
			break
		}
		p := NewPrompt(PromptEdit, fmt.Sprintf("edit %s: ", a.view().Headers()[col]), ce.CellValue(row, col), "")
		p.Row, p.Col = row, col
		a.prompt = p

	case "d":
		row := a.tbl.CursorRow()
		if row >= 0 && row < a.view().Len() {
			a.status.SetAction(fmt.Sprintf("Removing %q...", a.view().Item(row).SessionName()))
			return a, a.view().Remove(row)
		}

	case "enter":
		row := a.tbl.CursorRow()
		if row < 0 || row >= a.view().Len() {
			break
		}
		// Rows that can materialize (template rows in the tree) turn enter
		// into "name a workspace" rather than opening a tmux session.
		if mv, ok := a.view().(views.Materializer); ok && mv.CanMaterialize(row) {
			p := NewPrompt(PromptMaterialize, "workspace name: ", "", "workspace name")
			p.Row = row
			a.prompt = p
			break
		}
		{
			item := a.view().Item(row)
			if item.SessionPath() == "" {
				break // group rows and other non-openable items
			}
			if err := tmux.SessionFromPath(item.SessionName(), item.SessionPath(), item.Command()); err != nil {
				a.errMsg = fmt.Sprintf("tmux: %v", err)
			} else {
				a.status.SetAction(fmt.Sprintf("Opened %q", item.SessionName()))
			}
		}
	}
	return a, nil
}

func (a *App) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.prompt
	res, cmd := p.Update(msg)
	switch res {
	case PromptCancelled:
		a.prompt = nil

	case PromptSubmitted:
		a.prompt = nil
		value := p.Value()
		switch p.Kind {
		case PromptSearch:
			// Live search already moved the cursor; enter just closes.
		case PromptClone:
			if value != "" {
				if c, ok := a.view().(views.Cloner); ok {
					a.status.SetAction(fmt.Sprintf("Cloning %q...", value))
					return a, c.Clone(value)
				}
			}
		case PromptWorkspace:
			if value != "" {
				if wc, ok := a.view().(views.WorkspaceCreator); ok {
					a.status.SetAction(fmt.Sprintf("Creating workspace %q...", value))
					return a, wc.CreateWorkspace(value)
				}
			}
		case PromptEdit:
			if ce, ok := a.view().(views.CellEditor); ok {
				return a, ce.ApplyEdit(p.Row, p.Col, value)
			}
		case PromptTemplate:
			if value != "" {
				if tc, ok := a.view().(views.TemplateCreator); ok {
					a.status.SetAction(fmt.Sprintf("Saving template %q...", value))
					return a, tc.CreateTemplate(value)
				}
			}
		case PromptMaterialize:
			if value != "" {
				if mv, ok := a.view().(views.Materializer); ok {
					a.status.SetAction(fmt.Sprintf("Creating workspace %q...", value))
					return a, mv.Materialize(p.Row, value)
				}
			}
		case PromptRunTemplate:
			if value != "" {
				if tr, ok := a.view().(views.TemplateRunner); ok {
					a.status.SetAction(fmt.Sprintf("Editing prompt for %q...", value))
					return a, tr.RunWithPrompt(p.Row, value)
				}
			}
		}

	case PromptPending:
		if p.Kind == PromptSearch && p.Value() != "" {
			if idx := a.view().Match(strings.ToLower(p.Value())); idx >= 0 {
				a.tbl.SetCursor(idx)
			}
		}
	}
	return a, cmd
}

func (a *App) switchView(idx int) {
	a.active = idx
	v := a.view()
	a.tbl.Reset(v.Headers(), v.Ratios())
	a.tbl.SetRows(v.Rows())
	a.status.SetHelp(helpText(a.views, idx))
}

func (a *App) View() string {
	bottom := a.status.Render()
	if a.prompt != nil {
		bottom = a.prompt.View()
	}
	content := lipgloss.JoinVertical(lipgloss.Left, a.tbl.Render(), bottom)
	if a.errMsg != "" {
		content = RenderErrorOverlay(a.width, a.height, a.errMsg, content)
	}
	return content
}

// helpText builds the help string dynamically so view keys appear
// automatically.
func helpText(vs []views.View, activeIdx int) string {
	active := lipgloss.NewStyle().Bold(true).Foreground(theme.Yellow)
	parts := make([]string, 0, len(vs)+6)
	for i, v := range vs {
		label := fmt.Sprintf("%s: %s", v.Key(), v.Name())
		if i == activeIdx {
			label = active.Render(label)
		}
		parts = append(parts, label)
	}
	parts = append(parts, "r/m: expand/collapse", "space: select", "t: template",
		"w: workspace", "e: edit", "d: remove", "q: quit")
	return strings.Join(parts, " | ")
}
