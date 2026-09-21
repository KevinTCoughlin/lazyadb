package adb

import "testing"

func TestParseDevices(t *testing.T) {
	out := `List of devices attached
* daemon started successfully
R5CT30XXXXX            device product:b0q usb:1 model:SM_S908B device:b0q transport_id:3
192.168.1.40:5555      device product:redfin model:Pixel_5 device:redfin transport_id:7
emulator-5554          offline
ZY22XXXXXX             unauthorized
`

	devices := parseDevices(out)
	if len(devices) != 4 {
		t.Fatalf("got %d devices, want 4: %+v", len(devices), devices)
	}

	first := devices[0]
	if first.Serial != "R5CT30XXXXX" || first.State != "device" {
		t.Errorf("first device = %+v", first)
	}
	if first.Model != "SM_S908B" || first.TransportID != "3" {
		t.Errorf("first device metadata = %+v", first)
	}
	if got, want := first.Label(), "SM S908B"; got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}

	wireless := devices[1]
	if wireless.Serial != "192.168.1.40:5555" {
		t.Errorf("wireless serial = %q", wireless.Serial)
	}
	if !wireless.Online() {
		t.Error("wireless device should be online")
	}

	if devices[2].Online() || devices[3].Online() {
		t.Error("offline and unauthorized devices must not report Online")
	}
	if got, want := devices[3].Label(), "ZY22XXXXXX"; got != want {
		t.Errorf("Label() with no model = %q, want %q", got, want)
	}
}

func TestParseDevicesEmpty(t *testing.T) {
	if devices := parseDevices("List of devices attached\n\n"); len(devices) != 0 {
		t.Errorf("got %+v, want no devices", devices)
	}
}
