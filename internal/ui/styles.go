package ui

import "github.com/charmbracelet/lipgloss"

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

func levelRune(level byte) rune {
	if level == 0 {
		return ' '
	}
	return rune(level)
}
