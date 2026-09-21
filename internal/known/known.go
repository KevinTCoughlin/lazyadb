// Package known persists the wireless devices you have already paired with,
// so lazyadb can reattach to them on a later run.
//
// Pairing survives reboots on the device side, but the *connection* does not:
// a device drops off when it sleeps, changes network, or simply rotates its
// port. Re-pairing is not required in that case — reconnecting is — and the
// address to reconnect to is rarely the one you saved.
package known

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Device is one previously paired wireless device.
type Device struct {
	// DeviceID is the stable part of the mDNS instance name. It is the only
	// durable handle we have: the address changes, this does not.
	DeviceID string `json:"device_id"`
	// Addr is the last address we successfully connected to. It is a hint
	// for reconnecting, not an identity — the connect port is dynamic.
	Addr string `json:"addr"`
	// Label is the human-readable device name, for display only.
	Label string `json:"label,omitempty"`
	// LastConnected orders the list so the device you actually use is first.
	LastConnected time.Time `json:"last_connected"`
}

// Store is a JSON-backed set of known devices, keyed by DeviceID.
type Store struct {
	path string

	mu      sync.Mutex
	devices map[string]Device
}

// DefaultPath is where the store lives when none is configured.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazyadb", "known.json"), nil
}

// Load reads the store at path. A missing file is not an error — it just
// means nothing has been paired yet. A corrupt file is also not fatal: losing
// this cache costs one re-pair, while refusing to start costs the whole
// session, so it is reported and treated as empty.
func Load(path string) (*Store, error) {
	store := &Store{path: path, devices: make(map[string]Device)}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, fmt.Errorf("read %s: %w", path, err)
	}

	var devices []Device
	if err := json.Unmarshal(raw, &devices); err != nil {
		return store, fmt.Errorf("parse %s (ignoring it): %w", path, err)
	}
	for _, d := range devices {
		if d.DeviceID == "" {
			continue
		}
		store.devices[d.DeviceID] = d
	}
	return store, nil
}

// List returns known devices, most recently connected first.
func (s *Store) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastConnected.Equal(out[j].LastConnected) {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].LastConnected.After(out[j].LastConnected)
	})
	return out
}

// Remember records a successful connection, updating the stored address.
func (s *Store) Remember(d Device) error {
	if d.DeviceID == "" {
		return errors.New("device id is required")
	}
	if d.LastConnected.IsZero() {
		d.LastConnected = time.Now()
	}

	s.mu.Lock()
	// Keep an existing label if the caller didn't supply one; the label
	// comes from `adb devices -l`, which may not have caught up yet.
	if prev, ok := s.devices[d.DeviceID]; ok && d.Label == "" {
		d.Label = prev.Label
	}
	s.devices[d.DeviceID] = d
	s.mu.Unlock()

	return s.save()
}

// Forget drops a device from the store.
func (s *Store) Forget(deviceID string) error {
	s.mu.Lock()
	delete(s.devices, deviceID)
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.List(), "", "  ")
	if err != nil {
		return err
	}

	// Write via a temp file in the same directory so a crash mid-write can't
	// truncate the existing store.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".known-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
