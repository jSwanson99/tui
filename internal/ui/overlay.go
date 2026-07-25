package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"jds.net/tui/internal/theme"
)

// RenderErrorOverlay composites a centered error popup over bg.
func RenderErrorOverlay(width, height int, errMsg, bg string) string {
	maxWidth := max(width-8, 20)
	innerWidth := maxWidth - 6 // border (2) + padding (4)

	title := lipgloss.NewStyle().
		Foreground(theme.Red).Bold(true).
		Width(innerWidth).Background(theme.PopupBg).
		Render("Error")
	body := lipgloss.NewStyle().Width(innerWidth).Render(errMsg)
	footer := lipgloss.NewStyle().
		Foreground(theme.Overlay0).
		Width(innerWidth).Background(theme.PopupBg).
		Render("press esc to dismiss")
	inner := lipgloss.NewStyle().
		Width(innerWidth).Background(theme.PopupBg).Foreground(theme.Text).
		Render(lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", footer))

	popup := lipgloss.NewStyle().
		Background(theme.PopupBg).Foreground(theme.Text).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Lavender).BorderBackground(theme.PopupBg).
		Padding(0, 2).Width(maxWidth).
		Render(inner)

	// Pad lines so no background bleeds through.
	popupW := lipgloss.Width(popup)
	padStyle := lipgloss.NewStyle().Background(theme.PopupBg)
	pLines := strings.Split(popup, "\n")
	for i, line := range pLines {
		if w := lipgloss.Width(line); w < popupW {
			pLines[i] = line + padStyle.Render(strings.Repeat(" ", popupW-w))
		}
	}
	popup = strings.Join(pLines, "\n")

	x := max((width-popupW)/2, 0)
	y := max((height-lipgloss.Height(popup))/2, 0)
	return placeOverlay(x, y, popup, bg)
}

// placeOverlay places fg on top of bg at position (x, y), preserving ANSI
// sequences in the background lines it splits.
func placeOverlay(x, y int, fg, bg string) string {
	fgLines := strings.Split(fg, "\n")
	bgLines := strings.Split(bg, "\n")

	for i, fgLine := range fgLines {
		bgIdx := y + i
		if bgIdx < 0 || bgIdx >= len(bgLines) {
			continue
		}

		bgLine := bgLines[bgIdx]
		fgW := lipgloss.Width(fgLine)
		after := ""
		if x+fgW < lipgloss.Width(bgLine) {
			after = truncateLeft(bgLine, x+fgW)
		}

		left := truncateRight(bgLine, x)
		if pad := x - lipgloss.Width(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		bgLines[bgIdx] = left + fgLine + after
	}
	return strings.Join(bgLines, "\n")
}

// truncateRight returns the first n visible characters of s (preserving ANSI).
func truncateRight(s string, n int) string {
	var b strings.Builder
	visible := 0
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
		}
		if inEsc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			break
		}
		b.WriteRune(r)
		visible++
	}
	return b.String()
}

// truncateLeft returns everything after the first n visible characters of s.
func truncateLeft(s string, n int) string {
	visible := 0
	inEsc := false
	for i, r := range s {
		if r == '\x1b' {
			inEsc = true
		}
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			return s[i:]
		}
		visible++
	}
	return ""
}
