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

// logcatADB writes a fake adb whose device list is read from devicesFile and
// whose logcat records its arguments in callsFile, prints one line, and then
// either exits (when exitFile exists, standing in for a dropped device) or
// keeps streaming until killed.
func logcatADB(t *testing.T) (bin, devicesFile, callsFile, exitFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake adb script is POSIX-only")
	}
	dir := t.TempDir()
	devicesFile = filepath.Join(dir, "devices")
	callsFile = filepath.Join(dir, "calls")
	exitFile = filepath.Join(dir, "exit")
	script := `#!/bin/sh
if [ "$1" = "devices" ]; then
  echo "List of devices attached"
  cat "` + devicesFile + `"
  exit 0
fi
shift 2
echo "$*" >> "` + callsFile + `"
echo "I/Fake( 1): hello"
[ -e "` + exitFile + `" ] && exit 0
exec sleep 60
`
	bin = filepath.Join(dir, "adb")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, devicesFile, callsFile, exitFile
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitForCalls polls callsFile until it holds n logcat invocations.
func waitForCalls(t *testing.T, callsFile string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(callsFile)
		calls := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(data) > 0 && len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d logcat call(s), want %d: %q", len(calls), n, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitUntilNotTailing polls until serial's tail has removed itself.
func waitUntilNotTailing(t *testing.T, f *Fleet, serial string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		_, tailing := f.tails[serial]
		f.mu.Unlock()
		if !tailing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail for %s never ended", serial)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReattachStartsAtNewestLine(t *testing.T) {
	bin, devices, calls, _ := logcatADB(t)
	f := New(adb.New(bin), 64)
	defer f.Close()
	ctx := context.Background()

	writeFile(t, devices, "a\tdevice\n")
	if _, err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, calls, 1)

	// The device goes away and comes back across two refreshes.
	writeFile(t, devices, "")
	if _, err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	writeFile(t, devices, "a\tdevice\n")
	if _, err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	got := waitForCalls(t, calls, 2)
	if want := "logcat -v brief -T 200"; got[0] != want {
		t.Errorf("first attach = %q, want %q", got[0], want)
	}
	if want := "logcat -v brief -T 1"; got[1] != want {
		t.Errorf("reattach = %q, want %q", got[1], want)
	}
}

func TestTailThatEndsIsRestartedOnNextRefresh(t *testing.T) {
	bin, devices, calls, exit := logcatADB(t)
	f := New(adb.New(bin), 64)
	defer f.Close()
	ctx := context.Background()

	// logcat exits by itself, as it does when a device drops, and the device
	// is already back by the next refresh.
	writeFile(t, exit, "")
	writeFile(t, devices, "a\tdevice\n")
	if _, err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, calls, 1)
	waitUntilNotTailing(t, f, "a")

	if err := os.Remove(exit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	got := waitForCalls(t, calls, 2)
	if want := "logcat -v brief -T 1"; got[1] != want {
		t.Errorf("restarted tail = %q, want %q", got[1], want)
	}
}
