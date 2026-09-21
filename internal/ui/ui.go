// Package ui is the Bubble Tea front end: a device panel on the left, the
// merged fleet log on the right.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

var errNoTargets = errors.New("no online devices to install to")

type devicesMsg struct {
	devices []adb.Device
	err     error
}

type lineMsg fleet.Line

type tickMsg time.Time

type installMsg fleet.InstallStatus

// installClosedMsg arrives when every target has reached a terminal state.
type installClosedMsg struct{}

// mode is which input the keyboard is currently driving.
type mode int

const (
	modeNormal mode = iota
	modeFilter
	modeInstallPrompt
	modePairCode
	modePairAddr
)

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

	mode   mode
	input  string
	filter string

	install     *fleet.InstallRun
	installOpts adb.InstallOptions
	installJobs int
	installErr  error

	pairPanel    bool
	pairBusy     bool
	services     []adb.MDNSService
	pairSelected int
	pairAddr     string
	pairResult   *fleet.PairResult
	pairErr      error

	width, height int
	err           error
	quitting      bool
}

// New builds a Model. ctx governs the fleet's tails and is cancelled by the
// caller on exit.
func New(ctx context.Context, f *fleet.Fleet, interval time.Duration, installOpts adb.InstallOptions, installJobs int) Model {
	return Model{
		fleet:       f,
		ctx:         ctx,
		interval:    interval,
		allDevices:  true,
		installOpts: installOpts,
		installJobs: installJobs,
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

	case installMsg:
		// The run holds the authoritative per-device state; this message only
		// tells us to re-render and keep listening.
		return m, waitForInstall(m.install)

	case installClosedMsg:
		return m, nil

	case discoverMsg:
		// Only ever set an error here, never clear one: a discovery refresh
		// follows every pair attempt, and clobbering pairErr with this
		// message's nil would silently swallow a failed pair. Explicit
		// actions (rescan, reopening the panel) clear it instead.
		if msg.err != nil {
			m.pairErr = msg.err
		}
		m.services = msg.services
		if m.pairSelected >= len(m.services) {
			m.pairSelected = max(0, len(m.services)-1)
		}
		return m, nil

	case pairDoneMsg:
		m.pairBusy = false
		m.pairErr = msg.err
		if msg.err == nil {
			result := msg.result
			m.pairResult = &result
		}
		// A newly connected device should show up in the fleet immediately,
		// and the pairing service it was advertising is now gone.
		return m, tea.Batch(m.refresh(), m.discover())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func waitForInstall(run *fleet.InstallRun) tea.Cmd {
	if run == nil {
		return nil
	}
	return func() tea.Msg {
		status, ok := <-run.Updates()
		if !ok {
			return installClosedMsg{}
		}
		return installMsg(status)
	}
}

// installTargets is the fleet when scope is fleet-wide, or just the selected
// device otherwise. Offline and unauthorized devices are never targeted.
func (m Model) installTargets() []string {
	var serials []string
	for i, d := range m.devices {
		if !d.Online() {
			continue
		}
		if !m.allDevices && i != m.selected {
			continue
		}
		serials = append(serials, d.Serial)
	}
	return serials
}

func (m Model) startInstall(apk string) (Model, tea.Cmd) {
	m.installErr = nil
	run, err := m.fleet.Install(m.ctx, apk, m.installTargets(), m.installOpts, m.installJobs)
	if err != nil {
		m.installErr = err
		return m, nil
	}
	m.install = run
	return m, waitForInstall(run)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeNormal {
		return m.handleInputKey(msg)
	}
	if m.pairPanel {
		return m.handlePairKey(msg)
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
		m.mode = modeFilter
		m.input = m.filter
	case "i":
		if len(m.installTargets()) == 0 {
			m.installErr = errNoTargets
			return m, nil
		}
		m.mode = modeInstallPrompt
		m.input = ""
		m.installErr = nil
	case "w":
		m.pairPanel = true
		m.pairSelected = 0
		m.pairErr = nil
		m.pairResult = nil
		return m, m.discover()
	case "esc":
		// Cancel an install in flight; otherwise dismiss its results.
		if m.install != nil {
			if _, _, total := m.install.Tally(); total > 0 {
				m.install.Cancel()
			}
			if m.installComplete() {
				m.install = nil
			}
		}
		m.installErr = nil
	}
	return m, nil
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		input, mode := strings.TrimSpace(m.input), m.mode
		m.mode, m.input = modeNormal, ""
		switch mode {
		case modeFilter:
			m.filter = input
			return m, nil
		case modePairAddr:
			if input == "" {
				return m, nil
			}
			// Manual entry gives us the address; the code prompt comes next.
			m.pairAddr = input
			m.mode = modePairCode
			return m, nil
		case modePairCode:
			if input == "" {
				return m, nil
			}
			m.pairBusy = true
			m.pairErr = nil
			return m, m.pair(m.pairAddr, input)
		}
		if input == "" {
			return m, nil
		}
		return m.startInstall(expandHome(input))
	case tea.KeyEsc:
		if m.mode == modeFilter {
			m.filter = ""
		}
		m.mode, m.input = modeNormal, ""
	case tea.KeyBackspace:
		if m.input != "" {
			runes := []rune(m.input)
			m.input = string(runes[:len(runes)-1])
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	}
	return m, nil
}

// selectedFailure is the full adb failure for the highlighted device, or "".
func (m Model) selectedFailure() string {
	if m.install == nil || m.selected >= len(m.devices) {
		return ""
	}
	serial := m.devices[m.selected].Serial
	for _, s := range m.install.Statuses() {
		if s.Serial != serial || s.State != fleet.InstallFailed {
			continue
		}
		if s.Err != nil {
			return s.Err.Error()
		}
		return "install failed"
	}
	return ""
}

func (m Model) installComplete() bool {
	if m.install == nil {
		return false
	}
	done, _, total := m.install.Tally()
	return done == total
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
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

	if m.pairPanel {
		width := m.width - 4
		panel := panelStyle.Width(width).Height(bodyHeight).Render(m.pairPanelView(width, bodyHeight))
		return lipgloss.JoinVertical(lipgloss.Left, panel, m.pairStatusBar())
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
	title := "DEVICES"
	if m.install != nil {
		done, failed, total := m.install.Tally()
		title = fmt.Sprintf("INSTALL %d/%d", done, total)
		if failed > 0 {
			title += fmt.Sprintf(" (%d failed)", failed)
		}
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	if len(m.devices) == 0 {
		b.WriteString(dimStyle.Render("no devices attached"))
		return b.String()
	}

	statuses := m.installStatuses()

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

		// The second line is the device's serial normally, and its install
		// result while a run is on screen — that's the line you actually
		// want to read during a fan-out.
		if status, ok := statuses[d.Serial]; ok {
			b.WriteString("  " + m.renderInstallStatus(status))
		} else {
			b.WriteString(dimStyle.Render("  " + stateLabel(d)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) installStatuses() map[string]fleet.InstallStatus {
	if m.install == nil {
		return nil
	}
	out := make(map[string]fleet.InstallStatus)
	for _, s := range m.install.Statuses() {
		out[s.Serial] = s
	}
	return out
}

func (m Model) renderInstallStatus(s fleet.InstallStatus) string {
	glyph, style := installGlyph(s.State)
	label := s.State.String()
	switch s.State {
	case fleet.InstallRunning:
		label = fmt.Sprintf("installing %ds", int(s.Elapsed().Seconds()))
	case fleet.InstallSucceeded:
		label = fmt.Sprintf("ok %.1fs", s.Elapsed().Seconds())
	case fleet.InstallFailed:
		label = shortInstallError(s.Err)
	}
	return style.Render(glyph+" ") + dimStyle.Render(truncate(label, devicePanelWidth-6))
}

// shortInstallError picks the part of an adb failure worth showing in a 30
// column panel. "adb: failed to install app.apk: Failure [INSTALL_FAILED_
// INSUFFICIENT_STORAGE]" truncates to nothing useful, whereas the bracketed
// reason code is the bit you act on.
func shortInstallError(err error) string {
	if err == nil {
		return "failed"
	}
	msg := err.Error()
	if open := strings.LastIndex(msg, "["); open != -1 {
		if close := strings.Index(msg[open:], "]"); close != -1 {
			code := msg[open+1 : open+close]
			code = strings.TrimPrefix(code, "INSTALL_FAILED_")
			code = strings.TrimPrefix(code, "INSTALL_PARSE_FAILED_")
			if code != "" {
				return strings.ToLower(strings.ReplaceAll(code, "_", " "))
			}
		}
	}
	// No reason code: keep the tail, which is where adb puts the detail.
	if colon := strings.LastIndex(msg, ": "); colon != -1 && colon+2 < len(msg) {
		return msg[colon+2:]
	}
	return msg
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
	switch m.mode {
	case modeFilter:
		return statusStyle.Render("/" + m.input + "▎  enter: apply  esc: clear")
	case modeInstallPrompt:
		scope := fmt.Sprintf("%d device(s)", len(m.installTargets()))
		return statusStyle.Render("install apk → " + scope + ": " + m.input + "▎  enter: run  esc: cancel")
	}

	if m.installErr != nil {
		return errorStyle.Render("! " + m.installErr.Error())
	}
	if m.err != nil {
		return errorStyle.Render("! " + m.err.Error())
	}

	if m.install != nil {
		done, failed, total := m.install.Tally()
		state := fmt.Sprintf("install %s  %d/%d", filepath.Base(m.install.APK), done, total)
		if failed > 0 {
			state += fmt.Sprintf("  %d failed", failed)
		}
		hint := "esc: cancel"
		if done == total {
			hint = "esc: dismiss"
		}
		// The panel only has room for a reason code, so the status bar shows
		// the selected device's failure in full.
		if detail := m.selectedFailure(); detail != "" {
			return errorStyle.Render(state + "  │  " + truncate(detail, m.width-lipgloss.Width(state)-8))
		}
		return statusStyle.Render(state + "  │  " + hint)
	}

	state := fmt.Sprintf("%d device(s)", len(m.devices))
	if m.paused {
		state += "  PAUSED"
	}
	if dropped := m.fleet.Dropped(); dropped > 0 {
		state += fmt.Sprintf("  %d dropped", dropped)
	}
	help := "j/k: device  a: fleet/one  i: install  /: filter  p: pause  c: clear  q: quit"
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
