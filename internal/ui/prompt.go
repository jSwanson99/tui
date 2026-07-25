package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/theme"
)

// PromptKind identifies what the input is for; the app interprets the value
// on submit. This replaces the four near-identical mode branches.
type PromptKind int

const (
	PromptSearch PromptKind = iota
	PromptClone
	PromptWorkspace
	PromptEdit
	PromptTemplate    // name for saving the current selection as a template
	PromptMaterialize // workspace name when materializing a template row
	PromptRunTemplate // workspace name for materialize + editor prompt + opencode run
)

type PromptResult int

const (
	PromptPending PromptResult = iota
	PromptCancelled
	PromptSubmitted
)

type Prompt struct {
	Kind  PromptKind
	Label string
	Input textinput.Model

	// Edit context (PromptEdit only).
	Row, Col int
}

func NewPrompt(kind PromptKind, label, initial, placeholder string) *Prompt {
	in := textinput.New()
	in.Placeholder = placeholder
	in.SetValue(initial)
	in.Focus()
	return &Prompt{Kind: kind, Label: label, Input: in}
}

func (p *Prompt) Update(msg tea.KeyMsg) (PromptResult, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return PromptCancelled, nil
	case "enter":
		return PromptSubmitted, nil
	}
	var cmd tea.Cmd
	p.Input, cmd = p.Input.Update(msg)
	return PromptPending, cmd
}

func (p *Prompt) Value() string { return p.Input.Value() }

var promptStyle = lipgloss.NewStyle().
	Background(theme.Mantle).
	Foreground(theme.Text)

func (p *Prompt) View() string {
	return promptStyle.Render(p.Label + p.Input.View())
}
