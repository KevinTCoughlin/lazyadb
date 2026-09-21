package fleet

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
)

// fakeADB writes a script standing in for the adb binary. It succeeds for
// every serial except the ones named in failFor.
func fakeADB(t *testing.T, failFor ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake adb script is POSIX-only")
	}

	script := `#!/bin/sh
serial="$2"
case " ` + joinSpace(failFor) + ` " in
  *" $serial "*) echo "adb: failed to install app.apk: Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]"; exit 1 ;;
esac
echo "Success"
`
	path := filepath.Join(t.TempDir(), "adb")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func joinSpace(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func tempAPK(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(path, []byte("not really an apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func drain(run *InstallRun) {
	for range run.Updates() {
	}
}

func TestInstallFanOut(t *testing.T) {
	f := New(adb.New(fakeADB(t)), 16)
	run, err := f.Install(context.Background(), tempAPK(t), []string{"a", "b", "c"}, adb.InstallOptions{Reinstall: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	drain(run)

	done, failed, total := run.Tally()
	if total != 3 || done != 3 || failed != 0 {
		t.Fatalf("Tally() = %d/%d done, %d failed; want 3/3, 0", done, total, failed)
	}
	for _, s := range run.Statuses() {
		if s.State != InstallSucceeded {
			t.Errorf("%s: state = %v, err = %v", s.Serial, s.State, s.Err)
		}
		if s.Elapsed() <= 0 {
			t.Errorf("%s: Elapsed() = %v, want > 0", s.Serial, s.Elapsed())
		}
	}
}

// One device failing must not stop the others: a fleet install is only useful
// if it reports per-device outcomes.
func TestInstallOneFailureDoesNotAbortOthers(t *testing.T) {
	f := New(adb.New(fakeADB(t, "b")), 16)
	run, err := f.Install(context.Background(), tempAPK(t), []string{"a", "b", "c"}, adb.InstallOptions{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	drain(run)

	done, failed, total := run.Tally()
	if done != 3 || total != 3 || failed != 1 {
		t.Fatalf("Tally() = %d/%d done, %d failed; want 3/3, 1", done, total, failed)
	}

	byState := map[string]InstallState{}
	for _, s := range run.Statuses() {
		byState[s.Serial] = s.State
		if s.Serial == "b" {
			if s.Err == nil {
				t.Error("failed device carries no error")
			}
			if s.Output == "" {
				t.Error("failed device carries no adb output to show the user")
			}
		}
	}
	if byState["a"] != InstallSucceeded || byState["c"] != InstallSucceeded {
		t.Errorf("healthy devices did not finish: %v", byState)
	}
	if byState["b"] != InstallFailed {
		t.Errorf("failing device state = %v, want failed", byState["b"])
	}
}

func TestInstallRejectsBadInput(t *testing.T) {
	f := New(adb.New(fakeADB(t)), 16)

	if _, err := f.Install(context.Background(), tempAPK(t), nil, adb.InstallOptions{}, 0); err == nil {
		t.Error("installing to zero devices should fail fast")
	}
	if _, err := f.Install(context.Background(), filepath.Join(t.TempDir(), "missing.apk"), []string{"a"}, adb.InstallOptions{}, 0); err == nil {
		t.Error("a missing apk should fail before any device is touched")
	}
	if _, err := f.Install(context.Background(), t.TempDir(), []string{"a"}, adb.InstallOptions{}, 0); err == nil {
		t.Error("a directory should be rejected as an apk")
	}
}

func TestInstallCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before any worker starts

	f := New(adb.New(fakeADB(t)), 16)
	run, err := f.Install(ctx, tempAPK(t), []string{"a", "b"}, adb.InstallOptions{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	drain(run)

	done, failed, total := run.Tally()
	if done != total {
		t.Errorf("Tally() = %d/%d done; every device should reach a terminal state", done, total)
	}
	if failed != total {
		t.Errorf("failed = %d, want %d (cancelled counts as not-succeeded)", failed, total)
	}
}

func TestInstallConcurrencyLimit(t *testing.T) {
	f := New(adb.New(fakeADB(t)), 64)
	run, err := f.Install(context.Background(), tempAPK(t), []string{"a", "b", "c", "d"}, adb.InstallOptions{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	drain(run)

	if done, failed, total := run.Tally(); done != 4 || total != 4 || failed != 0 {
		t.Fatalf("Tally() = %d/%d done, %d failed; want 4/4, 0", done, total, failed)
	}
}
