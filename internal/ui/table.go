package ui

import (
	"log/slog"

	"github.com/76creates/stickers/table"
	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/theme"
)

// Table wraps the stickers table so cursor arithmetic and row rebuilding live
// in exactly one place.
type Table struct {
	t             *table.Table
	width, height int
	logger        *slog.Logger
}

func NewTable(headers []string, ratios []int, logger *slog.Logger) *Table {
	return &Table{t: newInner(headers, ratios), logger: logger}
}

func newInner(headers []string, ratios []int) *table.Table {
	t := table.NewTable(0, 0, headers)
	t.SetRatio(ratios)
	t.SetStylePassing(true)
	t.SetStyles(tableStyles())
	return t
}

// Reset swaps in a new table shape (used on view switch), preserving size.
func (tb *Table) Reset(headers []string, ratios []int) {
	tb.t = newInner(headers, ratios)
	tb.t.SetWidth(tb.width)
	tb.t.SetHeight(tb.height)
}

func (tb *Table) SetSize(w, h int) {
	tb.width, tb.height = w, h
	tb.t.SetWidth(w)
	tb.t.SetHeight(h)
}

func (tb *Table) SetRows(rows [][]any) {
	tb.t.ClearRows()
	if _, err := tb.t.AddRows(rows); err != nil && tb.logger != nil {
		tb.logger.Error("adding table rows", slog.Any("err", err))
	}
}

func (tb *Table) CursorPos() (col, row int) { return tb.t.GetCursorLocation() }

func (tb *Table) CursorRow() int {
	_, row := tb.t.GetCursorLocation()
	return row
}

// SetCursor moves the cursor to row, clamping at 0.
func (tb *Table) SetCursor(row int) {
	if row < 0 {
		row = 0
	}
	_, cur := tb.t.GetCursorLocation()
	for cur < row {
		tb.t.CursorDown()
		cur++
	}
	for cur > row {
		tb.t.CursorUp()
		cur--
	}
}

// Clamp pulls the cursor back into range after rows shrink.
func (tb *Table) Clamp(length int) {
	_, cur := tb.t.GetCursorLocation()
	for cur > 0 && cur > length-1 {
		tb.t.CursorUp()
		cur--
	}
}

func (tb *Table) Up()    { tb.t.CursorUp() }
func (tb *Table) Down()  { tb.t.CursorDown() }
func (tb *Table) Left()  { tb.t.CursorLeft() }
func (tb *Table) Right() { tb.t.CursorRight() }

func (tb *Table) Render() string { return tb.t.Render() }

func tableStyles() map[table.StyleKey]lipgloss.Style {
	return map[table.StyleKey]lipgloss.Style{
		table.StyleKeyHeader: lipgloss.NewStyle().
			Background(theme.Surface0).Foreground(theme.Lavender).Bold(true),
		table.StyleKeyFooter: lipgloss.NewStyle().
			Background(theme.Surface0).Foreground(theme.Overlay0).Align(lipgloss.Right),
		table.StyleKeyRows: lipgloss.NewStyle().
			Background(theme.Base).Foreground(theme.Text),
		table.StyleKeyRowsSubsequent: lipgloss.NewStyle().
			Background(theme.Mantle).Foreground(theme.Text),
		table.StyleKeyRowsCursor: lipgloss.NewStyle().
			Background(theme.Surface1).Foreground(theme.Text),
		table.StyleKeyCellCursor: lipgloss.NewStyle().
			Background(theme.Lavender).Foreground(theme.Crust).Bold(true),
	}
}
