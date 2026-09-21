package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

// connectWait bounds how long we wait for a freshly paired device to start
// advertising its connect service.
const connectWait = 10 * time.Second

type discoverMsg struct {
	services []adb.MDNSService
	err      error
}

type pairDoneMsg struct {
	result fleet.PairResult
	err    error
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
		if m.pairSelected < len(m.services)-1 {
			m.pairSelected++
		}
	case "k", "up":
		if m.pairSelected > 0 {
			m.pairSelected--
		}
	case "r":
		m.pairErr = nil
		return m, m.discover()
	case "m":
		// mDNS is unreliable across VLANs and locked-down corporate wifi, so
		// typing the address off the device's screen has to stay possible.
		m.mode = modePairAddr
		m.input = ""
		m.pairErr = nil
	case "enter":
		svc, ok := m.selectedService()
		if !ok {
			return m, nil
		}
		m.pairErr = nil
		m.pairResult = nil
		if svc.Connectable() {
			m.pairBusy = true
			return m, m.connect(svc.Addr)
		}
		m.pairAddr = svc.Addr
		m.mode = modePairCode
		m.input = ""
	}
	return m, nil
}

func (m Model) selectedService() (adb.MDNSService, bool) {
	if m.pairSelected < 0 || m.pairSelected >= len(m.services) {
		return adb.MDNSService{}, false
	}
	return m.services[m.pairSelected], true
}

func (m Model) pairPanelView(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("WIRELESS PAIRING"))
	b.WriteString("\n")

	if m.pairBusy {
		b.WriteString(dimStyle.Render("pairing…"))
		b.WriteString("\n")
	}

	if len(m.services) == 0 {
		b.WriteString(dimStyle.Render("no devices discovered over mDNS"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("On the device: Developer options → Wireless debugging"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("→ Pair device with pairing code, then press r to rescan"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("or m to type the address shown on screen."))
	} else {
		b.WriteString(m.serviceList(width, height))
	}

	if m.pairResult != nil {
		b.WriteString("\n")
		b.WriteString(m.pairResultView())
	}
	return b.String()
}

func (m Model) serviceList(width, height int) string {
	var b strings.Builder
	var lastGroup string

	rows := height - 6
	for i, svc := range m.services {
		if i >= rows {
			b.WriteString(dimStyle.Render(fmt.Sprintf("… %d more", len(m.services)-i)))
			break
		}

		group := "Ready to connect"
		if svc.Pairable() {
			group = "Waiting to pair"
		}
		if group != lastGroup {
			if lastGroup != "" {
				b.WriteString("\n")
			}
			b.WriteString(dimStyle.Render(group))
			b.WriteString("\n")
			lastGroup = group
		}

		marker, style := "  ", lipgloss.NewStyle()
		if i == m.pairSelected {
			marker, style = "▸ ", selectedStyle
		}
		line := fmt.Sprintf("%-22s %s", svc.Addr, svc.DeviceID())
		b.WriteString(style.Render(marker + truncate(line, width-6)))
		b.WriteString("\n")
	}
	return b.String()
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
		return statusStyle.Render(fmt.Sprintf("pairing code for %s: %s▎  enter: pair  esc: cancel", m.pairAddr, m.input))
	case modePairAddr:
		return statusStyle.Render("device address (host:port): " + m.input + "▎  enter: next  esc: cancel")
	}
	if m.pairErr != nil {
		return errorStyle.Render("! " + m.pairErr.Error())
	}
	if m.pairBusy {
		return statusStyle.Render("pairing…")
	}
	return statusStyle.Render("j/k: select  enter: pair/connect  m: manual  r: rescan  w/esc: close  q: quit")
}
