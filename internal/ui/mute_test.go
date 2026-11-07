package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

func press(m Model, key string) Model {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if key == " " {
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(key)}
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func send(m Model, serial, text string) Model {
	next, _ := m.Update(lineMsg(fleet.Line{Serial: serial, Level: 'I', Tag: "T", Text: text}))
	return next.(Model)
}

func TestPMutesSelectedDeviceWhileOthersFlow(t *testing.T) {
	m := modelWithLines(t, 0)
	m.devices = []adb.Device{{Serial: "noisy", State: "device"}, {Serial: "quiet", State: "device"}}

	m = press(m, "p")
	if m.paused {
		t.Fatal("p in the device panel paused the whole log instead of muting")
	}
	m = send(m, "noisy", "spam")
	m = send(m, "quiet", "useful")

	entries := m.visibleEntries()
	if len(entries) != 1 || entries[0].Line.Serial != "quiet" {
		t.Fatalf("visible = %+v, want only the unmuted device's line", entries)
	}
	if panel := m.devicePanel(20); !strings.Contains(panel, strings.TrimSpace(mutedMarker)) {
		t.Errorf("device panel shows no muted marker:\n%s", panel)
	}

	m = press(m, "p")
	m = send(m, "noisy", "back")
	if got := len(m.visibleEntries()); got != 2 {
		t.Errorf("after unmute %d entries visible, want 2", got)
	}
}

func TestPPausesFromLogPanelAndSpaceEverywhere(t *testing.T) {
	m := modelWithLines(t, 0)
	m.devices = []adb.Device{{Serial: "a", State: "device"}}

	m = press(m, " ")
	if !m.paused {
		t.Fatal("space should pause from the device panel")
	}
	m = press(m, " ")

	m.logFocus = true
	m = press(m, "p")
	if !m.paused {
		t.Error("p in the log panel should pause globally")
	}
	if m.logs.Muted("a") {
		t.Error("p in the log panel muted a device")
	}
}

func TestPIDFilterFromPrompt(t *testing.T) {
	m := modelWithLines(t, 0)
	for _, l := range []fleet.Line{
		{Serial: "a", Level: 'I', Tag: "App", PID: 3121, Text: "mine"},
		{Serial: "a", Level: 'I', Tag: "Sys", PID: 914, Text: "theirs"},
	} {
		next, _ := m.Update(lineMsg(l))
		m = next.(Model)
	}
	m.filter = "pid:3121"
	entries := m.visibleEntries()
	if len(entries) != 1 || entries[0].Line.PID != 3121 {
		t.Errorf("pid:3121 filter = %+v", entries)
	}
	if row := m.renderLine(entries[0].Line, 80); !strings.Contains(row, "App(3121)") {
		t.Errorf("rendered line should show the pid: %q", row)
	}
}
