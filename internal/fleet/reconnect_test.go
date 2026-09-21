package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/known"
)

// reconnectScript models a device whose connect port has moved since it was
// last saved — the normal case after a reboot or a network change.
const reconnectScript = `
case "$1" in
  devices)
    echo "List of devices attached"
    ;;
  mdns)
    echo "List of discovered mdns services"
    echo "adb-DEV1-aaa	_adb-tls-connect._tcp	10.0.0.5:51999"
    ;;
  connect)
    case "$2" in
      10.0.0.5:51999) echo "connected to $2" ;;
      *) echo "failed to connect to $2" ;;
    esac
    ;;
esac
`

// The saved address is a hint, not an identity. Reconnecting must follow the
// device ID to its current port rather than retrying the stale one.
func TestReconnectFollowsDeviceIDToItsNewPort(t *testing.T) {
	f := New(adb.New(fakePairADB(t, reconnectScript)), 8)

	results := f.Reconnect(context.Background(), []known.Device{
		{DeviceID: "DEV1", Addr: "10.0.0.5:42137", Label: "Pixel 5"},
	})

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Connected {
		t.Fatalf("Connected = false, err = %v", r.Err)
	}
	if r.Addr != "10.0.0.5:51999" {
		t.Errorf("Addr = %q, want the rediscovered port", r.Addr)
	}
	if !r.Rediscovered {
		t.Error("Rediscovered = false; the stored address was stale and should be flagged")
	}
}

// With no mDNS (corporate wifi, another VLAN) the stored address is all we
// have, and it must still be tried.
func TestReconnectFallsBackToStoredAddress(t *testing.T) {
	script := `
case "$1" in
  devices) echo "List of devices attached" ;;
  mdns) echo "List of discovered mdns services" ;;
  connect) echo "connected to $2" ;;
esac
`
	f := New(adb.New(fakePairADB(t, script)), 8)

	results := f.Reconnect(context.Background(), []known.Device{
		{DeviceID: "DEV1", Addr: "10.0.0.5:42137"},
	})
	if !results[0].Connected {
		t.Fatalf("Connected = false, err = %v", results[0].Err)
	}
	if results[0].Addr != "10.0.0.5:42137" {
		t.Errorf("Addr = %q, want the stored address", results[0].Addr)
	}
	if results[0].Rediscovered {
		t.Error("Rediscovered = true, but mDNS returned nothing")
	}
}

// An absent phone must not hold up or fail the rest of the fleet.
func TestReconnectIsolatesFailures(t *testing.T) {
	script := `
case "$1" in
  devices) echo "List of devices attached" ;;
  mdns) echo "List of discovered mdns services" ;;
  connect)
    case "$2" in
      10.0.0.5:1) echo "connected to $2" ;;
      *) echo "failed to connect to $2"; exit 1 ;;
    esac ;;
esac
`
	f := New(adb.New(fakePairADB(t, script)), 8)

	results := f.Reconnect(context.Background(), []known.Device{
		{DeviceID: "DEV1", Addr: "10.0.0.5:1"},
		{DeviceID: "DEV2", Addr: "10.0.0.9:2"},
	})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].Connected {
		t.Errorf("healthy device failed: %v", results[0].Err)
	}
	if results[1].Connected || results[1].Err == nil {
		t.Errorf("absent device should report an error, got %+v", results[1])
	}
	// Order must match the input so the caller can line results up.
	if results[0].Device.DeviceID != "DEV1" || results[1].Device.DeviceID != "DEV2" {
		t.Error("results are not in input order")
	}
}

// Reconnecting to something already attached should be a no-op, not a
// redundant adb call.
func TestReconnectSkipsAlreadyAttached(t *testing.T) {
	script := `
case "$1" in
  devices)
    echo "List of devices attached"
    echo "10.0.0.5:42137     device product:redfin model:Pixel_5 device:redfin transport_id:2"
    ;;
  mdns) echo "List of discovered mdns services" ;;
  connect) echo "failed to connect to $2"; exit 1 ;;
esac
`
	f := New(adb.New(fakePairADB(t, script)), 8)

	results := f.Reconnect(context.Background(), []known.Device{
		{DeviceID: "DEV1", Addr: "10.0.0.5:42137"},
	})
	if !results[0].Connected {
		t.Errorf("already-attached device reported as disconnected: %v", results[0].Err)
	}
}

func TestReconnectWithNoAddressAtAll(t *testing.T) {
	script := `
case "$1" in
  devices) echo "List of devices attached" ;;
  mdns) echo "List of discovered mdns services" ;;
esac
`
	f := New(adb.New(fakePairADB(t, script)), 8)

	results := f.Reconnect(context.Background(), []known.Device{{DeviceID: "DEV1"}})
	if results[0].Connected {
		t.Error("connected with no address?")
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "no address") {
		t.Errorf("Err = %v, want a no-address error", results[0].Err)
	}
}

func TestReconnectEmpty(t *testing.T) {
	f := New(adb.New(fakePairADB(t, reconnectScript)), 8)
	if got := f.Reconnect(context.Background(), nil); got != nil {
		t.Errorf("Reconnect(nil) = %+v, want nil", got)
	}
}
