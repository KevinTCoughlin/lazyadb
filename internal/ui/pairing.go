package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
	"github.com/kevintcoughlin/lazyadb/internal/known"
)

// connectWait bounds how long we wait for a freshly paired device to start
// advertising its connect service.
const connectWait = 10 * time.Second

// discoverInterval re-sweeps mDNS while the pairing panel is open, so a
// device you put into pairing mode shows up on its own rather than after a
// keypress you had to know about.
const discoverInterval = 2 * time.Second

type discoverMsg struct {
	services []adb.MDNSService
	err      error
}

// discoverTickMsg drives the auto-refresh while the panel is open.
type discoverTickMsg time.Time

type reconnectMsg struct {
	results []fleet.ReconnectResult
}

func discoverTick() tea.Cmd {
	return tea.Tick(discoverInterval, func(t time.Time) tea.Msg { return discoverTickMsg(t) })
}

// reconnectKnown reattaches to previously paired devices at startup.
func (m Model) reconnectKnown() tea.Cmd {
	if m.store == nil {
		return nil
	}
	devices := m.store.List()
	if len(devices) == 0 {
		return nil
	}
	return func() tea.Msg {
		return reconnectMsg{results: m.fleet.Reconnect(m.ctx, devices)}
	}
}

type pairDoneMsg struct {
	result fleet.PairResult
	err    error
}

// knownConnectMsg is the outcome of enter on a "Known, not advertising" row.
type knownConnectMsg struct {
	result fleet.ReconnectResult
}

// forgetMsg is the outcome of d on a "Known, not advertising" row.
type forgetMsg struct {
	err error
}

// pairRowKind is which group a pairing-panel row belongs to.
type pairRowKind int

const (
	rowPairable pairRowKind = iota
	rowConnectable
	rowKnown
)

func (k pairRowKind) String() string {
	switch k {
	case rowPairable:
		return "Waiting to pair"
	case rowConnectable:
		return "Ready to connect"
	}
	return "Known, not advertising"
}

// pairRow is one selectable line in the pairing panel: either a live mDNS
// advertisement or a remembered device that is not currently advertising.
type pairRow struct {
	kind    pairRowKind
	service adb.MDNSService // rowPairable, rowConnectable
	device  known.Device    // rowKnown
}

// pairRows merges what mDNS is advertising right now with the devices we have
// paired before, grouped in display order.
//
// Devices are keyed by mDNS device ID, the only identity that survives a port
// change. A known device that is advertising under that ID is listed once, as
// its live row, because the live row carries the current port. An exact
// address match also counts as advertising: some adb builds rotate the mDNS
// instance name after pairing, and the same host:port is the same endpoint
// whatever it is called.
func pairRows(services []adb.MDNSService, devices []known.Device) []pairRow {
	advertisingIDs := make(map[string]bool, len(services))
	advertisingAddrs := make(map[string]bool, len(services))
	var pairable, connectable []pairRow
	for _, svc := range services {
		advertisingIDs[svc.DeviceID()] = true
		advertisingAddrs[svc.Addr] = true
		switch {
		case svc.Pairable():
			pairable = append(pairable, pairRow{kind: rowPairable, service: svc})
		case svc.Connectable():
			connectable = append(connectable, pairRow{kind: rowConnectable, service: svc})
		}
	}

	rows := make([]pairRow, 0, len(pairable)+len(connectable)+len(devices))
	rows = append(rows, pairable...)
	rows = append(rows, connectable...)
	for _, d := range devices {
		if advertisingIDs[d.DeviceID] || (d.Addr != "" && advertisingAddrs[d.Addr]) {
			continue
		}
		rows = append(rows, pairRow{kind: rowKnown, device: d})
	}
	return rows
}

// knownDevices is the remembered set, or nil without a store.
func (m Model) knownDevices() []known.Device {
	if m.store == nil {
		return nil
	}
	return m.store.List()
}

func (m Model) pairRows() []pairRow { return pairRows(m.services, m.knownDevices()) }

// clampPairSelection keeps the cursor on a row after the list shrinks.
func (m Model) clampPairSelection() Model {
	if n := len(m.pairRows()); m.pairSelected >= n {
		m.pairSelected = max(0, n-1)
	}
	return m
}

// relativeTime renders how long ago t was, coarsely: this is for "is this the
// phone I used this morning or last month", not for timing anything.
func relativeTime(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
	return t.Format("2006-01-02")
}

func (m Model) discover() tea.Cmd {
	return func() tea.Msg {
		services, err := m.fleet.Discover(m.ctx)
		return discoverMsg{services: services, err: err}
	}
}

func (m Model) pair(addr, code string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.fleet.PairAndConnect(m.ctx, addr, code, connectWait)
		return pairDoneMsg{result: result, err: err}
	}
}

func (m Model) connect(addr string) tea.Cmd {
	return func() tea.Msg {
		out, err := m.fleet.Connect(m.ctx, addr)
		return pairDoneMsg{
			result: fleet.PairResult{ConnectAddr: addr, Connected: err == nil, Output: out},
			err:    err,
		}
	}
}

// connectKnown tries a remembered device at its saved address. It is
// fleet.Reconnect's fallback path: the row exists because mDNS is silent about
// this device, so there is nothing to rediscover.
func (m Model) connectKnown(device known.Device) tea.Cmd {
	return func() tea.Msg {
		return knownConnectMsg{result: m.fleet.ConnectStored(m.ctx, device)}
	}
}

// forget drops a remembered device. There is no connection to tear down: the
// row only exists because the device is not advertising.
func (m Model) forget(device known.Device) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if store == nil {
			return forgetMsg{}
		}
		return forgetMsg{err: store.Forget(device.DeviceID)}
	}
}

func (m Model) disconnect(svc adb.MDNSService) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		err := m.fleet.Disconnect(m.ctx, svc.Addr)
		if err == nil && store != nil {
			// Forgetting is best-effort: a store write failing should not be
			// reported as a failed disconnect, because the disconnect worked.
			_ = store.Forget(svc.DeviceID())
		}
		return pairDoneMsg{result: fleet.PairResult{ConnectAddr: svc.Addr}, err: err}
	}
}

// handlePairKey drives the pairing panel while it is open and no text input
// is active.
func (m Model) handlePairKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc", "w":
		m.pairPanel = false
		m.pairErr = nil
		m.pairResult = nil
		return m, nil
	case "j", "down":
		if m.pairSelected < len(m.pairRows())-1 {
			m.pairSelected++
		}
	case "k", "up":
		if m.pairSelected > 0 {
			m.pairSelected--
		}
	case "r":
		m.pairErr = nil
		return m, m.discover()
	case "d":
		// Disconnect and forget. Pairing itself lives on the device, so this
		// only drops the connection and our memory of it.
		row, ok := m.selectedRow()
		if !ok || row.kind == rowPairable {
			return m, nil
		}
		m.pairErr = nil
		m.pairResult = nil
		if row.kind == rowKnown {
			return m, m.forget(row.device)
		}
		return m, m.disconnect(row.service)
	case "m":
		// mDNS is unreliable across VLANs and locked-down corporate wifi, so
		// typing the address off the device's screen has to stay possible.
		m = m.openPrompt(modePairAddr)
		m.pairErr = nil
	case "enter":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.pairErr = nil
		m.pairResult = nil
		switch row.kind {
		case rowConnectable:
			m.pairBusy = true
			return m, m.connect(row.service.Addr)
		case rowKnown:
			m.pairBusy = true
			return m, m.connectKnown(row.device)
		}
		m.pairAddr = row.service.Addr
		m = m.openPrompt(modePairCode)
	}
	return m, nil
}

func (m Model) selectedRow() (pairRow, bool) {
	rows := m.pairRows()
	if m.pairSelected < 0 || m.pairSelected >= len(rows) {
		return pairRow{}, false
	}
	return rows[m.pairSelected], true
}

func (m Model) pairPanelView(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("WIRELESS PAIRING"))
	b.WriteString("\n")

	if m.pairBusy {
		b.WriteString(dimStyle.Render("pairing…"))
		b.WriteString("\n")
	}

	if summary := m.reconnectSummary(); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}

	rows := m.pairRows()
	switch {
	case len(rows) == 0:
		// Nothing advertising and nothing remembered: this is a first run,
		// so say how to get a device into pairing mode.
		b.WriteString(dimStyle.Render("no devices discovered over mDNS, and nothing paired yet"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("On the device: Developer options → Wireless debugging"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("→ Pair device with pairing code, then press r to rescan"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("or m to type the address shown on screen."))
	case len(m.services) == 0:
		// Paired before, but nothing is advertising. Say so explicitly, or
		// this reads as a broken discovery rather than absent devices.
		b.WriteString(dimStyle.Render(fmt.Sprintf(
			"no devices discovered over mDNS; none of %d known device(s) is advertising", len(rows))))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("They may be asleep, on another network, or behind wifi that blocks mDNS."))
		b.WriteString("\n\n")
		b.WriteString(m.pairRowList(rows, width, height-3))
	default:
		b.WriteString(m.pairRowList(rows, width, height))
	}

	if m.pairResult != nil {
		b.WriteString("\n")
		b.WriteString(m.pairResultView())
	}
	return b.String()
}

func (m Model) pairRowList(rows []pairRow, width, height int) string {
	var b strings.Builder
	lastGroup := pairRowKind(-1)
	now := time.Now()

	limit := height - 6
	for i, row := range rows {
		if i >= limit {
			b.WriteString(dimStyle.Render(fmt.Sprintf("… %d more", len(rows)-i)))
			break
		}

		if row.kind != lastGroup {
			if lastGroup != -1 {
				b.WriteString("\n")
			}
			b.WriteString(dimStyle.Render(row.kind.String()))
			b.WriteString("\n")
			lastGroup = row.kind
		}

		marker, style := "  ", lipgloss.NewStyle()
		if i == m.pairSelected {
			marker, style = "▸ ", selectedStyle
		}
		b.WriteString(style.Render(marker + truncate(m.pairRowText(row, now), width-6)))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) pairRowText(row pairRow, now time.Time) string {
	if row.kind != rowKnown {
		return fmt.Sprintf("%-22s %s", row.service.Addr, row.service.DeviceID())
	}
	d := row.device
	label := d.Label
	if label == "" {
		label = d.DeviceID
	}
	addr := d.Addr
	if addr == "" {
		addr = "(no saved address)"
	}
	// A device reattached at startup through its saved address is in the
	// fleet even though mDNS can't see it; "last seen" would undersell that.
	seen := "last seen " + relativeTime(now, d.LastConnected)
	if m.isAttached(d.Addr) {
		seen = "attached"
	}
	return fmt.Sprintf("%-22s %-16s %s", addr, label, seen)
}

func (m Model) isAttached(serial string) bool {
	if serial == "" {
		return false
	}
	for _, d := range m.devices {
		if d.Serial == serial && d.Online() {
			return true
		}
	}
	return false
}

// reconnectSummary reports what startup reconnection did. Rediscovered
// devices are called out specifically, because "it came back on a different
// port" is the thing a user would otherwise waste time discovering by hand.
func (m Model) reconnectSummary() string {
	if len(m.reconnected) == 0 {
		return ""
	}
	var connected, moved, failed int
	for _, r := range m.reconnected {
		switch {
		case r.Connected && r.Rediscovered:
			connected++
			moved++
		case r.Connected:
			connected++
		default:
			failed++
		}
	}

	msg := fmt.Sprintf("reattached %d/%d known device(s)", connected, len(m.reconnected))
	if moved > 0 {
		msg += fmt.Sprintf(", %d on a new port", moved)
	}
	if failed > 0 {
		msg += fmt.Sprintf(", %d unreachable", failed)
	}
	return dimStyle.Render(msg)
}

func (m Model) pairResultView() string {
	r := m.pairResult
	if !r.Connected {
		return ""
	}
	msg := fmt.Sprintf("✓ connected %s", r.ConnectAddr)
	if r.PairAddr != "" && r.PairAddr != r.ConnectAddr {
		// Worth showing: the port you connect on is not the port you paired
		// on, and this is the step the panel exists to remove.
		msg += dimStyle.Render(fmt.Sprintf("  (paired on %s)", r.PairAddr))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render(msg)
}

func (m Model) pairStatusBar() string {
	switch m.mode {
	case modePairCode:
		return statusStyle.Render(fmt.Sprintf("pairing code for %s: %s  enter: pair  esc: cancel", m.pairAddr, m.input.View()))
	case modePairAddr:
		return statusStyle.Render("device address (host:port): " + m.input.View() + "  enter: next  esc: cancel")
	}
	if m.pairErr != nil {
		return errorStyle.Render("! " + m.pairErr.Error())
	}
	if m.pairBusy {
		return statusStyle.Render("pairing…")
	}
	return statusStyle.Render("j/k: select  enter: pair/connect  d: disconnect/forget  m: manual  r: rescan  w/esc: close  q: quit")
}
