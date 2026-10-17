package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
)

// modelWithLines returns a sized Model holding n plain log lines.
func modelWithLines(t *testing.T, n int) Model {
	t.Helper()
	var model tea.Model = New(context.Background(), fleet.New(nil, 1), time.Second, adb.InstallOptions{}, 0, nil)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	for i := range n {
		model, _ = model.Update(lineMsg(fleet.Line{Serial: "a", Level: 'I', Tag: "T", Text: fmt.Sprintf("line %d", i)}))
	}
	return model.(Model)
}

func wheel(m Model, button tea.MouseButton, x int) Model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: 5, Button: button, Action: tea.MouseActionPress})
	return next.(Model)
}

func TestWheelOverLogMovesEntryCursor(t *testing.T) {
	m := modelWithLines(t, 50)
	logX := devicePanelWidth + 10

	m = wheel(m, tea.MouseButtonWheelUp, logX)
	if !m.logFocus {
		t.Fatal("wheel over the log should focus it")
	}
	if want := 49 - wheelStep; m.logSel != want {
		t.Fatalf("logSel = %d, want %d", m.logSel, want)
	}
	// The viewport must keep the selected entry on screen.
	if row := m.logSel; row < m.vp.YOffset || row >= m.vp.YOffset+m.vp.Height {
		t.Errorf("selected row %d outside viewport [%d, %d)", row, m.vp.YOffset, m.vp.YOffset+m.vp.Height)
	}

	m = wheel(m, tea.MouseButtonWheelDown, logX)
	m = wheel(m, tea.MouseButtonWheelDown, logX)
	if m.logSel != -1 {
		t.Errorf("wheeling back to the bottom should follow the tail, logSel = %d", m.logSel)
	}
}

func TestWheelOverDevicePanelMovesDeviceSelection(t *testing.T) {
	m := modelWithLines(t, 0)
	m.devices = []adb.Device{{Serial: "a"}, {Serial: "b"}}

	m = wheel(m, tea.MouseButtonWheelDown, 5)
	m = wheel(m, tea.MouseButtonWheelDown, 5)
	if m.selected != 1 {
		t.Errorf("selected = %d, want 1 (clamped at the last device)", m.selected)
	}
	m = wheel(m, tea.MouseButtonWheelUp, 5)
	m = wheel(m, tea.MouseButtonWheelUp, 5)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 (clamped at the first device)", m.selected)
	}
	if m.logFocus {
		t.Error("wheel over the device panel should not focus the log")
	}
}

func TestWheelIgnoredWhilePromptOpen(t *testing.T) {
	m := modelWithLines(t, 50).openPrompt(modeFilter)

	m = wheel(m, tea.MouseButtonWheelUp, devicePanelWidth+10)
	if m.logSel != -1 || m.logFocus {
		t.Errorf("wheel moved the log behind an open prompt: logSel = %d, logFocus = %v", m.logSel, m.logFocus)
	}
}
