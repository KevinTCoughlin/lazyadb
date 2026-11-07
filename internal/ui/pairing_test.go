package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
	"github.com/kevintcoughlin/lazyadb/internal/known"
)

func TestPairRowsMergesKnownDevicesWithoutDuplicates(t *testing.T) {
	services := []adb.MDNSService{
		{Name: "adb-LIVE-aaa", Type: adb.ServiceConnect, Addr: "10.0.0.5:51999"},
		{Name: "adb-NEW-bbb", Type: adb.ServicePairing, Addr: "10.0.0.7:37000"},
	}
	devices := []known.Device{
		// Advertising on a new port: the live row must win, once.
		{DeviceID: "LIVE", Addr: "10.0.0.5:42137", Label: "Pixel 5"},
		// Not advertising at all.
		{DeviceID: "ASLEEP", Addr: "10.0.0.9:40000", Label: "Tablet"},
		// Advertising under a rotated instance name at the same address.
		{DeviceID: "ROTATED", Addr: "10.0.0.5:51999"},
	}

	rows := pairRows(services, devices)

	var kinds []pairRowKind
	for _, r := range rows {
		kinds = append(kinds, r.kind)
	}
	want := []pairRowKind{rowPairable, rowConnectable, rowKnown}
	if len(kinds) != len(want) {
		t.Fatalf("got %d rows (%v), want %d", len(rows), kinds, len(want))
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("row kinds = %v, want %v (pair, connect, known order)", kinds, want)
		}
	}
	if rows[1].service.Addr != "10.0.0.5:51999" {
		t.Errorf("advertising device listed at %q, want its live address", rows[1].service.Addr)
	}
	if rows[2].device.DeviceID != "ASLEEP" {
		t.Errorf("known row = %q, want only the device that is not advertising", rows[2].device.DeviceID)
	}
}

func TestPairRowsGroupsInterleavedServices(t *testing.T) {
	services := []adb.MDNSService{
		{Name: "adb-A-1", Type: adb.ServiceConnect, Addr: "10.0.0.1:1"},
		{Name: "adb-B-1", Type: adb.ServicePairing, Addr: "10.0.0.2:1"},
		{Name: "adb-C-1", Type: adb.ServiceConnect, Addr: "10.0.0.3:1"},
	}
	rows := pairRows(services, nil)
	if rows[0].kind != rowPairable || rows[1].kind != rowConnectable || rows[2].kind != rowConnectable {
		t.Errorf("rows not grouped: %+v", rows)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "never"},
		{now.Add(-20 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-2*time.Hour - 10*time.Minute), "2h ago"},
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(-90 * 24 * time.Hour), "2025-12-10"},
		// Clock skew: a timestamp slightly in the future is still "just now".
		{now.Add(time.Minute), "just now"},
	}
	for _, tt := range tests {
		if got := relativeTime(now, tt.at); got != tt.want {
			t.Errorf("relativeTime(%v) = %q, want %q", tt.at, got, tt.want)
		}
	}
}

// pairModel returns a sized Model with the pairing panel open over store.
func pairModel(t *testing.T, store *known.Store) Model {
	t.Helper()
	var model tea.Model = New(context.Background(), fleet.New(nil, 1), time.Second, adb.InstallOptions{}, 0, store)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m := model.(Model)
	m.pairPanel = true
	return m
}

func TestEmptyPairPanelDistinguishesNeverPairedFromUnreachable(t *testing.T) {
	store, err := known.Load(filepath.Join(t.TempDir(), "known.json"))
	if err != nil {
		t.Fatal(err)
	}

	view := pairModel(t, store).pairPanelView(100, 28)
	if !strings.Contains(view, "nothing paired yet") {
		t.Errorf("empty store should read as never paired:\n%s", view)
	}

	if err := store.Remember(known.Device{DeviceID: "DEV1", Addr: "10.0.0.5:1", Label: "Pixel 5"}); err != nil {
		t.Fatal(err)
	}
	view = pairModel(t, store).pairPanelView(100, 28)
	for _, want := range []string{"none of 1 known device(s) is advertising", "Known, not advertising", "10.0.0.5:1", "Pixel 5", "last seen just now"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "nothing paired yet") {
		t.Errorf("known devices should not read as never paired:\n%s", view)
	}
}

func TestForgetKnownRow(t *testing.T) {
	store, err := known.Load(filepath.Join(t.TempDir(), "known.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember(known.Device{DeviceID: "DEV1", Addr: "10.0.0.5:1"}); err != nil {
		t.Fatal(err)
	}
	m := pairModel(t, store)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("d on a known row should forget it")
	}
	next, _ = next.Update(cmd())
	if got := store.List(); len(got) != 0 {
		t.Errorf("store still holds %+v after d", got)
	}
	if rows := next.(Model).pairRows(); len(rows) != 0 {
		t.Errorf("panel still lists %+v", rows)
	}
}
