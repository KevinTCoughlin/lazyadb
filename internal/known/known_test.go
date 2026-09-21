package known

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "known.json")

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("fresh store = %+v, want empty", got)
	}

	now := time.Now().Truncate(time.Second)
	if err := store.Remember(Device{DeviceID: "DEV1", Addr: "10.0.0.5:42137", Label: "Pixel 5", LastConnected: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember(Device{DeviceID: "DEV2", Addr: "10.0.0.6:35111", LastConnected: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	devices := reloaded.List()
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	// Most recently connected first, so the device you actually use leads.
	if devices[0].DeviceID != "DEV2" {
		t.Errorf("order = %s first, want DEV2", devices[0].DeviceID)
	}
	if devices[1].Label != "Pixel 5" {
		t.Errorf("label not persisted: %+v", devices[1])
	}
}

// The address is expected to change; the device ID is the identity.
func TestRememberUpdatesAddressKeepingLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Remember(Device{DeviceID: "DEV1", Addr: "10.0.0.5:42137", Label: "Pixel 5"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember(Device{DeviceID: "DEV1", Addr: "10.0.0.5:51999"}); err != nil {
		t.Fatal(err)
	}

	devices := store.List()
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1 (same device, new port)", len(devices))
	}
	if devices[0].Addr != "10.0.0.5:51999" {
		t.Errorf("Addr = %q, want the new port", devices[0].Addr)
	}
	if devices[0].Label != "Pixel 5" {
		t.Errorf("Label = %q, want the previous label kept", devices[0].Label)
	}
}

func TestForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known.json")
	store, _ := Load(path)
	_ = store.Remember(Device{DeviceID: "DEV1", Addr: "10.0.0.5:1"})

	if err := store.Forget("DEV1"); err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 0 {
		t.Errorf("after Forget: %+v, want empty", got)
	}

	reloaded, _ := Load(path)
	if got := reloaded.List(); len(got) != 0 {
		t.Errorf("Forget did not persist: %+v", got)
	}
}

func TestRememberRequiresDeviceID(t *testing.T) {
	store, _ := Load(filepath.Join(t.TempDir(), "known.json"))
	if err := store.Remember(Device{Addr: "10.0.0.5:1"}); err == nil {
		t.Error("a device with no ID should be rejected")
	}
}

// Losing this cache costs one re-pair; refusing to start costs the session.
func TestLoadCorruptFileIsNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := Load(path)
	if err == nil {
		t.Error("corrupt file should be reported")
	}
	if store == nil {
		t.Fatal("corrupt file must still yield a usable store")
	}
	if got := store.List(); len(got) != 0 {
		t.Errorf("corrupt store = %+v, want empty", got)
	}
	// And it must still be writable afterwards.
	if err := store.Remember(Device{DeviceID: "DEV1", Addr: "10.0.0.5:1"}); err != nil {
		t.Errorf("store unusable after corrupt load: %v", err)
	}
}

func TestLoadMissingFileIsClean(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Errorf("missing file should not be an error, got %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}
