package fleet

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
)

// fakePairADB stands in for adb during a pairing handshake. The connect
// service is only advertised once pairing has happened, mirroring real
// devices — and on a different port than pairing, which is the whole point.
func fakePairADB(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake adb script is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "adb")
	full := "#!/bin/sh\nSTATE=\"" + filepath.Join(dir, "paired") + "\"\n" + script
	if err := os.WriteFile(path, []byte(full), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const pairScript = `
case "$1" in
  mdns)
    echo "List of discovered mdns services"
    if [ -f "$STATE" ]; then
      echo "adb-DEV123-aaa	_adb-tls-connect._tcp	192.168.1.40:42137"
    else
      echo "adb-DEV123-aaa	_adb-tls-pairing._tcp	192.168.1.40:37113"
    fi
    ;;
  pair)
    if [ "$3" = "123456" ]; then
      touch "$STATE"
      echo "Successfully paired to 192.168.1.40:37113 [guid=adb-DEV123]"
    else
      echo "Failed: wrong password"
    fi
    ;;
  connect)
    echo "connected to $2"
    ;;
esac
`

func TestPairAndConnectResolvesTheConnectPort(t *testing.T) {
	f := New(adb.New(fakePairADB(t, pairScript)), 8)

	result, err := f.PairAndConnect(context.Background(), "192.168.1.40:37113", "123456", 3*time.Second)
	if err != nil {
		t.Fatalf("PairAndConnect() error = %v (output %q)", err, result.Output)
	}
	if !result.Connected {
		t.Error("Connected = false")
	}
	// The payoff: the user typed the pairing port, and we attached to the
	// connect port without them having to look it up.
	if got, want := result.ConnectAddr, "192.168.1.40:42137"; got != want {
		t.Errorf("ConnectAddr = %q, want %q", got, want)
	}
	if result.ConnectAddr == result.PairAddr {
		t.Error("connect address should differ from the pairing address")
	}
}

// adb pair exits 0 even on a wrong code, so a naive implementation reports
// success and then leaves the user with an unpaired device.
func TestPairAndConnectRejectsWrongCode(t *testing.T) {
	f := New(adb.New(fakePairADB(t, pairScript)), 8)

	result, err := f.PairAndConnect(context.Background(), "192.168.1.40:37113", "999999", time.Second)
	if err == nil {
		t.Fatal("wrong pairing code reported success")
	}
	if result.Connected {
		t.Error("Connected = true after a failed pair")
	}
	if strings.Contains(err.Error(), "paired, but") {
		t.Errorf("wrong code misreported as a connect problem: %v", err)
	}
}

func TestPairAndConnectRequiresCode(t *testing.T) {
	f := New(adb.New(fakePairADB(t, pairScript)), 8)
	if _, err := f.PairAndConnect(context.Background(), "192.168.1.40:37113", "  ", time.Second); err == nil {
		t.Error("empty pairing code should fail before touching adb")
	}
}

// If pairing works but the connect service never shows up, the user must be
// told they are paired — sending them back to re-enter a working code is the
// wrong recovery.
func TestPairSucceedsButConnectServiceNeverAppears(t *testing.T) {
	script := `
case "$1" in
  mdns)
    echo "List of discovered mdns services"
    echo "adb-DEV123-aaa	_adb-tls-pairing._tcp	192.168.1.40:37113"
    ;;
  pair) echo "Successfully paired to 192.168.1.40:37113 [guid=adb-DEV123]" ;;
  connect) echo "connected to $2" ;;
esac
`
	f := New(adb.New(fakePairADB(t, script)), 8)

	_, err := f.PairAndConnect(context.Background(), "192.168.1.40:37113", "123456", 700*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error when no connect service appears")
	}
	if !strings.HasPrefix(err.Error(), "paired, but") {
		t.Errorf("error = %q, want it to make clear pairing itself succeeded", err)
	}
	if !strings.Contains(err.Error(), "adb connect") {
		t.Errorf("error = %q, want a manual-recovery hint", err)
	}
}

func TestMatchConnectFallsBackToHost(t *testing.T) {
	services := []adb.MDNSService{
		{Name: "adb-OTHER-zzz", Type: adb.ServiceConnect, Addr: "192.168.1.99:5555"},
		{Name: "adb-ROTATED-bbb", Type: adb.ServiceConnect, Addr: "192.168.1.40:42137"},
	}
	// The device ID rotated after pairing, so the ID match misses and the
	// host match has to carry it.
	if got, _ := matchConnect(services, "DEV123", "192.168.1.40"); got != "192.168.1.40:42137" {
		want := "192.168.1.40:42137"
		t.Errorf("matchConnect() = %q, want %q", got, want)
	}
	if got, _ := matchConnect(services, "DEV123", "10.0.0.1"); got != "" {
		t.Errorf("matchConnect() = %q, want empty for an unknown host", got)
	}
}
