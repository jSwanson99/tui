package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/theme"
)

// StatusBar renders help on the left and the last action on the right.
type StatusBar struct {
	width  int
	help   string
	action string
}

func NewStatusBar() *StatusBar { return &StatusBar{} }

func (s *StatusBar) SetWidth(w int)       { s.width = w }
func (s *StatusBar) SetHelp(help string)  { s.help = help }
func (s *StatusBar) SetAction(act string) { s.action = act }

var actionStyle = lipgloss.NewStyle().Bold(true).Foreground(theme.Green)

func (s *StatusBar) Render() string {
	help := s.help
	action := actionStyle.Render(s.action)
	gap := s.width - lipgloss.Width(help) - lipgloss.Width(action)
	if gap < 1 {
		gap = 1
	}
	return help + strings.Repeat(" ", gap) + action
}
