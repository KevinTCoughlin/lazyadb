// Package ui is the Bubble Tea front end: a device panel on the left, the
// merged fleet log on the right.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

const (
	devicePanelWidth = 30
	maxBufferedLines = 5000
)

type devicesMsg struct {
	devices []adb.Device
	err     error
}

type lineMsg fleet.Line

type tickMsg time.Time

// Model is the root Bubble Tea model.
type Model struct {
	fleet    *fleet.Fleet
	ctx      context.Context
	interval time.Duration

	devices  []adb.Device
	selected int
	// allDevices shows the whole fleet; when false the log is filtered to the
	// selected device.
	allDevices bool

	lines  []fleet.Line
	paused bool

	filtering bool
	filter    string

	width, height int
	err           error
	quitting      bool
}

// New builds a Model. ctx governs the fleet's tails and is cancelled by the
// caller on exit.
func New(ctx context.Context, f *fleet.Fleet, interval time.Duration) Model {
	return Model{
		fleet:      f,
		ctx:        ctx,
		interval:   interval,
		allDevices: true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), waitForLine(m.fleet.Lines()), tick(m.interval))
}

func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		devices, err := m.fleet.Refresh(m.ctx)
		return devicesMsg{devices: devices, err: err}
	}
}

func waitForLine(ch <-chan fleet.Line) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return lineMsg(line)
	}
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refresh(), tick(m.interval))

	case devicesMsg:
		m.err = msg.err
		m.devices = msg.devices
		if m.selected >= len(m.devices) {
			m.selected = max(0, len(m.devices)-1)
		}
		return m, nil

	case lineMsg:
		if !m.paused {
			m.lines = append(m.lines, fleet.Line(msg))
			if len(m.lines) > maxBufferedLines {
				m.lines = m.lines[len(m.lines)-maxBufferedLines:]
			}
		}
		return m, waitForLine(m.fleet.Lines())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch msg.Type {
		case tea.KeyEnter:
			m.filtering = false
		case tea.KeyEsc:
			m.filtering, m.filter = false, ""
		case tea.KeyBackspace:
			if m.filter != "" {
				m.filter = m.filter[:len(m.filter)-1]
			}
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(msg.Runes)
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		if m.selected < len(m.devices)-1 {
			m.selected++
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
		}
	case "a":
		m.allDevices = !m.allDevices
	case "p", " ":
		m.paused = !m.paused
	case "c":
		m.lines = nil
	case "r":
		return m, m.refresh()
	case "/":
		m.filtering = true
		m.filter = ""
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "starting lazyadb…"
	}

	// Each panel's lipgloss Width covers its padding but not its border, so
	// both borders (2 columns each) come out of the log panel's share.
	logWidth := m.width - devicePanelWidth - 4
	if logWidth < 20 {
		logWidth = 20
	}
	bodyHeight := m.height - 2
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		panelStyle.Width(devicePanelWidth).Height(bodyHeight).Render(m.devicePanel(bodyHeight)),
		panelStyle.Width(logWidth).Height(bodyHeight).Render(m.logPanel(logWidth, bodyHeight)),
	)
	return lipgloss.JoinVertical(lipgloss.Left, body, m.statusBar())
}

func (m Model) devicePanel(height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("DEVICES"))
	b.WriteString("\n")

	if len(m.devices) == 0 {
		b.WriteString(dimStyle.Render("no devices attached"))
		return b.String()
	}

	rows := height - 3
	for i, d := range m.devices {
		if i >= rows {
			b.WriteString(dimStyle.Render(fmt.Sprintf("… %d more", len(m.devices)-i)))
			break
		}
		marker := "  "
		style := lipgloss.NewStyle()
		if i == m.selected {
			marker = "▸ "
			style = selectedStyle
		}
		name := truncate(d.Label(), devicePanelWidth-6)
		b.WriteString(style.Render(marker + name))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + stateLabel(d)))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) logPanel(width, height int) string {
	scope := "fleet"
	if !m.allDevices && len(m.devices) > 0 {
		scope = m.devices[m.selected].Label()
	}
	header := titleStyle.Render("LOGCAT") + dimStyle.Render("  "+scope)
	if m.filter != "" {
		header += dimStyle.Render("  /" + m.filter)
	}

	visible := m.visibleLines()
	rows := height - 3
	if len(visible) > rows {
		visible = visible[len(visible)-rows:]
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	for _, line := range visible {
		b.WriteString(m.renderLine(line, width-2))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) visibleLines() []fleet.Line {
	var serial string
	if !m.allDevices && len(m.devices) > 0 {
		serial = m.devices[m.selected].Serial
	}
	filter := strings.ToLower(m.filter)

	out := make([]fleet.Line, 0, len(m.lines))
	for _, line := range m.lines {
		if serial != "" && line.Serial != serial {
			continue
		}
		if filter != "" &&
			!strings.Contains(strings.ToLower(line.Text), filter) &&
			!strings.Contains(strings.ToLower(line.Tag), filter) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func (m Model) renderLine(line fleet.Line, width int) string {
	// The device column is what makes a merged fleet log readable; each
	// device gets a stable color derived from its position in the list.
	device := truncate(m.labelFor(line.Serial), 10)
	col := deviceStyle(m.indexOf(line.Serial)).Render(fmt.Sprintf("%-10s", device))
	level := levelStyle(line.Level).Render(string(levelRune(line.Level)))

	text := line.Text
	if line.Tag != "" {
		text = dimStyle.Render(line.Tag+": ") + text
	}
	rendered := col + " " + level + " " + text
	return truncateStyled(rendered, width)
}

func (m Model) labelFor(serial string) string {
	for _, d := range m.devices {
		if d.Serial == serial {
			return d.Label()
		}
	}
	return serial
}

func (m Model) indexOf(serial string) int {
	for i, d := range m.devices {
		if d.Serial == serial {
			return i
		}
	}
	return 0
}

func (m Model) statusBar() string {
	if m.filtering {
		return statusStyle.Render("/" + m.filter + "▎  enter: apply  esc: clear")
	}
	if m.err != nil {
		return errorStyle.Render("! " + m.err.Error())
	}

	state := fmt.Sprintf("%d device(s)", len(m.devices))
	if m.paused {
		state += "  PAUSED"
	}
	if dropped := m.fleet.Dropped(); dropped > 0 {
		state += fmt.Sprintf("  %d dropped", dropped)
	}
	help := "j/k: device  a: fleet/one  /: filter  p: pause  c: clear  r: rescan  q: quit"
	return statusStyle.Render(state + "  │  " + help)
}

func stateLabel(d adb.Device) string {
	if d.Online() {
		return d.Serial
	}
	return d.State
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:max(0, width-1)]) + "…"
}

// truncateStyled trims a string that already carries ANSI styling, which
// lipgloss measures correctly but plain slicing would corrupt.
func truncateStyled(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}
