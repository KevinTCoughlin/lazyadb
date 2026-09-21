package adb

import "testing"

func TestParseMDNSServices(t *testing.T) {
	out := `List of discovered mdns services
adb-39121FDJG004TC-vWgJpq	_adb-tls-pairing._tcp	192.168.1.40:37113
adb-39121FDJG004TC-vWgJpq	_adb-tls-connect._tcp	192.168.1.40:42137
adb-R5CT30ABCDE-ZxQ1mn	_adb._tcp	192.168.1.55:5555
garbage line with too few
`

	services := parseMDNSServices(out)
	if len(services) != 3 {
		t.Fatalf("got %d services, want 3: %+v", len(services), services)
	}

	pairing := services[0]
	if !pairing.Pairable() || pairing.Connectable() {
		t.Errorf("pairing service classified wrong: %+v", pairing)
	}
	if got, want := pairing.Addr, "192.168.1.40:37113"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}

	connect := services[1]
	if !connect.Connectable() || connect.Pairable() {
		t.Errorf("connect service classified wrong: %+v", connect)
	}

	// The pairing and connect services are the same physical device on
	// different ports; the device ID is what links them.
	if pairing.DeviceID() != connect.DeviceID() {
		t.Errorf("DeviceID mismatch: %q vs %q", pairing.DeviceID(), connect.DeviceID())
	}
	if got, want := pairing.DeviceID(), "39121FDJG004TC"; got != want {
		t.Errorf("DeviceID() = %q, want %q", got, want)
	}
	if pairing.Addr == connect.Addr {
		t.Error("pairing and connect ports should differ; the test fixture is wrong")
	}

	if !services[2].Connectable() {
		t.Error("legacy _adb._tcp should count as connectable")
	}
}

func TestParseMDNSServicesEmpty(t *testing.T) {
	if got := parseMDNSServices("List of discovered mdns services\n"); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}
