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

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
	"github.com/kevintcoughlin/lazyadb/internal/known"
	"github.com/kevintcoughlin/lazyadb/internal/logbuf"
)

const (
	devicePanelWidth = 30
	maxBufferedLines = 5000
	// wheelStep is how many log entries one mouse-wheel notch moves.
	wheelStep = 3
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

	logs *logbuf.Buffer
	// vp clips and scrolls the rendered log. The entry cursor below is still
	// ours, because folding needs a selected *entry* and a viewport only
	// knows about rows.
	vp      viewport.Model
	vpReady bool
	paused  bool
	// logFocus sends the arrow/jk keys to the log instead of the device list.
	logFocus bool
	// logSel is the scroll cursor into the filtered view; negative follows
	// the tail, which is the default and what you want while watching live.
	logSel   int
	minLevel byte

	mode mode
	// input backs every prompt (filter, apk path, pairing code). bubbles
	// handles the cursor, unicode, and editing keys that are tedious and
	// easy to get subtly wrong by hand.
	input  textinput.Model
	filter string

	install     *fleet.InstallRun
	installOpts adb.InstallOptions
	installJobs int
	installErr  error

	store        *known.Store
	reconnected  []fleet.ReconnectResult
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
func New(ctx context.Context, f *fleet.Fleet, interval time.Duration, installOpts adb.InstallOptions, installJobs int, store *known.Store) Model {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 1024

	return Model{
		input:       input,
		fleet:       f,
		ctx:         ctx,
		interval:    interval,
		allDevices:  true,
		installOpts: installOpts,
		installJobs: installJobs,
		store:       store,
		logs:        logbuf.New(maxBufferedLines),
		logSel:      -1,
	}
}

func (m Model) Init() tea.Cmd {
	// Reattaching to known wireless devices runs alongside the first device
	// scan rather than blocking it, so USB devices show up immediately.
	return tea.Batch(m.refresh(), waitForLine(m.fleet.Lines()), tick(m.interval), m.reconnectKnown())
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
		logWidth, bodyHeight := m.logDimensions()
		if m.vpReady {
			m.vp.Width, m.vp.Height = logWidth-2, bodyHeight-2
		} else {
			m.vp = viewport.New(logWidth-2, bodyHeight-2)
			m.vpReady = true
		}
		return m.refreshLogView(), nil

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
			m.logs.Append(fleet.Line(msg))
			m = m.refreshLogView()
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

	case discoverTickMsg:
		// Only keep sweeping while the panel is actually open.
		if !m.pairPanel {
			return m, nil
		}
		return m, tea.Batch(m.discover(), discoverTick())

	case reconnectMsg:
		m.reconnected = msg.results
		m.persistReconnects(msg.results)
		// A device that came back should appear in the fleet right away.
		return m, m.refresh()

	case pairDoneMsg:
		m.pairBusy = false
		m.pairErr = msg.err
		if msg.err == nil {
			result := msg.result
			m.pairResult = &result
			m.rememberPaired(result)
		}
		// A newly connected device should show up in the fleet immediately,
		// and the pairing service it was advertising is now gone.
		return m, tea.Batch(m.refresh(), m.discover())

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
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
	case "tab":
		m.logFocus = !m.logFocus
		if !m.logFocus {
			// Leaving the log resumes following the tail, so you never come
			// back later to a view frozen somewhere in the past.
			m.logSel = -1
		}
		return m.refreshLogView(), nil
	case "j", "down":
		if m.logFocus {
			return m.scrollLog(1).refreshLogView(), nil
		}
		if m.selected < len(m.devices)-1 {
			m.selected++
		}
	case "k", "up":
		if m.logFocus {
			return m.scrollLog(-1).refreshLogView(), nil
		}
		if m.selected > 0 {
			m.selected--
		}
	case "ctrl+d", "pgdown":
		if m.logFocus {
			return m.scrollLog(10).refreshLogView(), nil
		}
	case "ctrl+u", "pgup":
		if m.logFocus {
			return m.scrollLog(-10).refreshLogView(), nil
		}
	case "g":
		if m.logFocus {
			m.logSel = 0
			return m.refreshLogView(), nil
		}
	case "G":
		if m.logFocus {
			m.logSel = -1
			return m.refreshLogView(), nil
		}
	case "enter":
		// Fold or unfold the selected stack trace.
		if m.logFocus {
			entries := m.visibleEntries()
			if sel := m.logSelection(len(entries)); sel >= 0 && entries[sel].IsTrace() {
				entries[sel].Expanded = !entries[sel].Expanded
			}
			return m.refreshLogView(), nil
		}
	case "e":
		m.minLevel = nextMinLevel(m.minLevel)
		return m.refreshLogView(), nil
	case "a":
		m.allDevices = !m.allDevices
		return m.refreshLogView(), nil
	case "p", " ":
		m.paused = !m.paused
	case "c":
		m.logs.Clear()
		m.logSel = -1
		return m.refreshLogView(), nil
	case "r":
		return m, m.refresh()
	case "/":
		m = m.openPrompt(modeFilter)
		m.input.SetValue(m.filter)
	case "i":
		if len(m.installTargets()) == 0 {
			m.installErr = errNoTargets
			return m, nil
		}
		m = m.openPrompt(modeInstallPrompt)
		m.installErr = nil
	case "w":
		m.pairPanel = true
		m.pairSelected = 0
		m.pairErr = nil
		m.pairResult = nil
		return m, tea.Batch(m.discover(), discoverTick())
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

// handleMouse maps the wheel onto the same cursors the keyboard drives, so the
// wheel never scrolls the viewport out from under the entry cursor and enter
// always folds what is on screen. Mouse events only arrive with -mouse, since
// mouse reporting costs plain drag-to-select in most terminals.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeNormal || m.pairPanel || msg.Action != tea.MouseActionPress {
		return m, nil
	}

	var delta int
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -1
	case tea.MouseButtonWheelDown:
		delta = 1
	default:
		return m, nil
	}

	// The device panel is devicePanelWidth wide plus its two border columns.
	if msg.X < devicePanelWidth+2 {
		m.selected = min(max(m.selected+delta, 0), max(len(m.devices)-1, 0))
		return m, nil
	}

	// Wheeling over the log focuses it, as tab does, so the cursor it moves
	// is the one on screen.
	m.logFocus = true
	return m.scrollLog(delta * wheelStep).refreshLogView(), nil
}

// scrollLog moves the cursor by delta entries, clamping at the ends. Reaching
// the bottom drops back into follow-the-tail rather than pinning to the last
// entry, so live output keeps flowing.
func (m Model) scrollLog(delta int) Model {
	entries := m.visibleEntries()
	if len(entries) == 0 {
		return m
	}

	current := m.logSelection(len(entries))
	next := current + delta
	switch {
	case next < 0:
		next = 0
	case next >= len(entries)-1:
		m.logSel = -1
		return m
	}
	m.logSel = next
	return m
}

// nextMinLevel cycles all → warnings → errors → all. Those are the three
// settings anyone actually uses when watching a fleet.
func nextMinLevel(level byte) byte {
	switch level {
	case 0:
		return 'W'
	case 'W':
		return 'E'
	}
	return 0
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		value, mode := strings.TrimSpace(m.input.Value()), m.mode
		m = m.closePrompt()
		switch mode {
		case modeFilter:
			m.filter = value
			return m.refreshLogView(), nil
		case modePairAddr:
			if value == "" {
				return m, nil
			}
			// Manual entry gives us the address; the code prompt comes next.
			m.pairAddr = value
			return m.openPrompt(modePairCode), nil
		case modePairCode:
			if value == "" {
				return m, nil
			}
			m.pairBusy = true
			m.pairErr = nil
			return m, m.pair(m.pairAddr, value)
		}
		if value == "" {
			return m, nil
		}
		return m.startInstall(expandHome(value))

	case tea.KeyEsc:
		if m.mode == modeFilter {
			m.filter = ""
			m = m.refreshLogView()
		}
		return m.closePrompt(), nil
	}

	// Everything else — editing keys, unicode, cursor movement — is the
	// component's job.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// openPrompt focuses the shared input for a mode, starting empty.
func (m Model) openPrompt(mode mode) Model {
	m.mode = mode
	m.input.SetValue("")
	m.input.Focus()
	return m
}

func (m Model) closePrompt() Model {
	m.mode = modeNormal
	m.input.SetValue("")
	m.input.Blur()
	return m
}

// persistReconnects writes back the address a device actually answered on.
//
// This matters precisely when mDNS is unavailable: the stored address is the
// only fallback then, so leaving a known-stale one on disk defeats the point
// of storing it at all. It also refreshes LastConnected, which orders the list.
func (m Model) persistReconnects(results []fleet.ReconnectResult) {
	if m.store == nil {
		return
	}
	for _, r := range results {
		if !r.Connected || r.Addr == "" {
			continue
		}
		if !r.Rediscovered && r.Addr == r.Device.Addr && !r.Device.LastConnected.IsZero() {
			continue
		}
		_ = m.fleet.RememberConnected(m.store, r.Device.DeviceID, r.Addr, r.Device.Label)
	}
}

// rememberPaired records a newly connected wireless device so a later run can
// reattach to it. Without a device ID there is nothing durable to store — the
// address alone goes stale — so that case is skipped rather than saved.
func (m Model) rememberPaired(result fleet.PairResult) {
	if m.store == nil || !result.Connected || result.DeviceID == "" {
		return
	}
	var label string
	for _, d := range m.devices {
		if d.Serial == result.ConnectAddr {
			label = d.Label()
			break
		}
	}
	// A store write failing must not derail a successful pair; the panel
	// already shows the connection, and the cost is re-pairing next time.
	_ = m.fleet.RememberConnected(m.store, result.DeviceID, result.ConnectAddr, label)
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

	logWidth, bodyHeight := m.logDimensions()

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

	if m.minLevel != 0 {
		header += dimStyle.Render("  ≥" + string(m.minLevel))
	}
	if m.logFocus {
		header += dimStyle.Render("  [scroll]")
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(m.vp.View())
	return b.String()
}

// logDimensions is the log panel's outer width and the body height. Each
// panel's lipgloss Width covers its padding but not its border, so both
// borders (2 columns each) come out of the log panel's share.
func (m Model) logDimensions() (logWidth, bodyHeight int) {
	logWidth = m.width - devicePanelWidth - 4
	if logWidth < 20 {
		logWidth = 20
	}
	bodyHeight = m.height - 2
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	return logWidth, bodyHeight
}

// renderLogRows flattens entries to display rows, expanding open traces, and
// returns the row index of the selected entry so the viewport can be scrolled
// to keep it visible.
func (m Model) renderLogRows(width int) (rows []string, cursorRow int) {
	entries := m.visibleEntries()
	selection := m.logSelection(len(entries))
	cursorRow = -1

	rows = make([]string, 0, len(entries))
	for i, entry := range entries {
		selected := m.logFocus && i == selection
		if selected {
			cursorRow = len(rows)
		}
		rows = append(rows, m.renderEntry(entry, width, selected))
		if entry.Expanded {
			for _, frame := range entry.Frames {
				rows = append(rows, dimStyle.Render("             "+truncate(frame, width-14)))
			}
		}
	}
	return rows, cursorRow
}

// refreshLogView re-renders the log into the viewport. Called whenever the
// content or the selection changes, keeping View() free of mutation.
func (m Model) refreshLogView() Model {
	if !m.vpReady {
		return m
	}

	rows, cursorRow := m.renderLogRows(m.vp.Width)
	m.vp.SetContent(strings.Join(rows, "\n"))

	if !m.logFocus || m.logSel < 0 {
		// Following the tail is the default and what you want while watching
		// live output.
		m.vp.GotoBottom()
		return m
	}
	// Scroll only as far as needed to bring the cursor into view, so paging
	// around doesn't jump the window more than it has to.
	switch {
	case cursorRow < m.vp.YOffset:
		m.vp.SetYOffset(cursorRow)
	case cursorRow >= m.vp.YOffset+m.vp.Height:
		m.vp.SetYOffset(cursorRow - m.vp.Height + 1)
	}
	return m
}

// visibleEntries applies the device scope, text filter, and minimum level.
func (m Model) visibleEntries() []*logbuf.Entry {
	var serial string
	if !m.allDevices && len(m.devices) > 0 {
		serial = m.devices[m.selected].Serial
	}
	return m.logs.View(logbuf.Filter{
		Serial:   serial,
		Text:     m.filter,
		MinLevel: m.minLevel,
	})
}

// logSelection resolves the scroll cursor. -1 means following the tail.
func (m Model) logSelection(count int) int {
	if count == 0 {
		return -1
	}
	if m.logSel < 0 || m.logSel >= count {
		return count - 1
	}
	return m.logSel
}

func (m Model) renderEntry(entry *logbuf.Entry, width int, selected bool) string {
	rendered := m.renderLine(entry.Line, width)
	if entry.IsTrace() && !entry.Expanded {
		label := "frames"
		if len(entry.Frames) == 1 {
			label = "frame"
		}
		rendered = truncateStyled(rendered, width-16) +
			dimStyle.Render(fmt.Sprintf("  ▸ %d %s", len(entry.Frames), label))
	}
	if selected {
		return selectedStyle.Render("▸") + rendered
	}
	if m.logFocus {
		return " " + rendered
	}
	return rendered
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
		return statusStyle.Render("/" + m.input.View() + "  enter: apply  esc: clear")
	case modeInstallPrompt:
		scope := fmt.Sprintf("%d device(s)", len(m.installTargets()))
		return statusStyle.Render("install apk → " + scope + ": " + m.input.View() + "  enter: run  esc: cancel")
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
	help := "tab: scroll log  j/k: device  a: fleet/one  e: level  i: install  /: filter  p: pause  q: quit"
	if m.logFocus {
		help = "j/k: scroll  ^u/^d: page  g/G: top/bottom  enter: fold  e: level  tab: devices  q: quit"
	}
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
