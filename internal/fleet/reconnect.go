package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/known"
)

// ReconnectResult is one known device's reattachment attempt.
type ReconnectResult struct {
	Device known.Device
	// Addr is the address we actually connected to, which is often not the
	// address that was stored.
	Addr         string
	Connected    bool
	Rediscovered bool
	Err          error
}

// Reconnect reattaches to previously paired devices.
//
// The saved address is only a hint. A wireless device's connect port is
// dynamic — it changes across reboots and network moves — so a stored
// host:port goes stale routinely, and blindly retrying it is the reason
// reconnecting by hand is tedious. mDNS is consulted first and the device ID
// is matched, which survives the port change; the stored address is the
// fallback for when mDNS is unavailable (locked-down wifi, other VLAN).
//
// Devices already attached are skipped, and failures are per-device: an
// absent phone must not hold up the rest.
func (f *Fleet) Reconnect(ctx context.Context, devices []known.Device) []ReconnectResult {
	if len(devices) == 0 {
		return nil
	}

	attached := f.attachedAddrs(ctx)

	// One mDNS sweep shared by every device, rather than one per device.
	var services []adb.MDNSService
	if found, err := f.client.MDNSServices(ctx); err == nil {
		services = found
	}

	results := make([]ReconnectResult, len(devices))
	var wg sync.WaitGroup
	for i, device := range devices {
		wg.Add(1)
		go func(i int, device known.Device) {
			defer wg.Done()
			results[i] = f.reconnectOne(ctx, device, services, attached)
		}(i, device)
	}
	wg.Wait()
	return results
}

// ConnectStored attaches to one known device at its saved address without
// consulting mDNS. It is exactly Reconnect's fallback path, for when the
// caller already knows the device is not advertising — the pairing panel's
// "Known, not advertising" rows — so a sweep would only cost time.
func (f *Fleet) ConnectStored(ctx context.Context, device known.Device) ReconnectResult {
	return f.reconnectOne(ctx, device, nil, f.attachedAddrs(ctx))
}

func (f *Fleet) reconnectOne(ctx context.Context, device known.Device, services []adb.MDNSService, attached map[string]bool) ReconnectResult {
	result := ReconnectResult{Device: device, Addr: device.Addr}

	// Prefer a live mDNS advertisement for this device ID over the stored
	// address, since the port it carries is the current one.
	for _, svc := range services {
		if svc.Connectable() && svc.DeviceID() == device.DeviceID {
			if svc.Addr != device.Addr {
				result.Rediscovered = true
			}
			result.Addr = svc.Addr
			break
		}
	}

	if result.Addr == "" {
		result.Err = errNoAddress
		return result
	}
	if attached[result.Addr] {
		result.Connected = true
		return result
	}

	if _, err := f.client.Connect(ctx, result.Addr); err != nil {
		result.Err = err
		return result
	}
	result.Connected = true
	return result
}

// attachedAddrs is the set of device serials already attached. For wireless
// devices the serial *is* the host:port, which is what lets us skip work.
func (f *Fleet) attachedAddrs(ctx context.Context) map[string]bool {
	attached := make(map[string]bool)
	devices, err := f.client.Devices(ctx)
	if err != nil {
		return attached
	}
	for _, d := range devices {
		if d.Online() {
			attached[d.Serial] = true
		}
	}
	return attached
}

// RememberConnected records a device we just connected to, so a later run can
// reattach without re-pairing.
func (f *Fleet) RememberConnected(store *known.Store, deviceID, addr, label string) error {
	if store == nil || deviceID == "" {
		return nil
	}
	return store.Remember(known.Device{
		DeviceID:      deviceID,
		Addr:          addr,
		Label:         label,
		LastConnected: time.Now(),
	})
}

// Disconnect detaches a wireless device without forgetting it.
func (f *Fleet) Disconnect(ctx context.Context, addr string) error {
	return f.client.Disconnect(ctx, addr)
}
