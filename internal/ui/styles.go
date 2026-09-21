package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

var (
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))

	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)

	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(0, 1)

	// deviceColors give each device in the fleet a stable, distinguishable
	// color in the merged log's device column.
	deviceColors = []lipgloss.Color{"117", "150", "215", "183", "210", "229", "152", "180"}

	levelColors = map[byte]lipgloss.Color{
		'V': "244",
		'D': "117",
		'I': "150",
		'W': "215",
		'E': "203",
		'F': "197",
	}
)

func deviceStyle(index int) lipgloss.Style {
	color := deviceColors[index%len(deviceColors)]
	return lipgloss.NewStyle().Foreground(color)
}

func levelStyle(level byte) lipgloss.Style {
	color, ok := levelColors[level]
	if !ok {
		color = "244"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(color)
}

// installGlyph maps an install state to a marker and color. The glyphs are
// ASCII-safe on purpose: this panel is the one you read across a room while
// six devices flash, and box-drawing spinners render badly over ssh.
func installGlyph(state fleet.InstallState) (string, lipgloss.Style) {
	switch state {
	case fleet.InstallRunning:
		return "»", lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	case fleet.InstallSucceeded:
		return "✓", lipgloss.NewStyle().Foreground(lipgloss.Color("150"))
	case fleet.InstallFailed:
		return "✗", lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	case fleet.InstallCancelled:
		return "-", lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	}
	return "·", dimStyle
}

func levelRune(level byte) rune {
	if level == 0 {
		return ' '
	}
	return rune(level)
}
