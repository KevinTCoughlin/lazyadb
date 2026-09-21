package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
)

// InstallState is where one device is in a fan-out install.
type InstallState int

const (
	InstallPending InstallState = iota
	InstallRunning
	InstallSucceeded
	InstallFailed
	InstallCancelled
)

func (s InstallState) String() string {
	switch s {
	case InstallPending:
		return "pending"
	case InstallRunning:
		return "installing"
	case InstallSucceeded:
		return "ok"
	case InstallFailed:
		return "failed"
	case InstallCancelled:
		return "cancelled"
	}
	return "unknown"
}

// Done reports whether the device has reached a terminal state.
func (s InstallState) Done() bool {
	return s == InstallSucceeded || s == InstallFailed || s == InstallCancelled
}

// InstallStatus is one device's progress. adb install gives no incremental
// byte count worth trusting, so progress is state plus elapsed time rather
// than a percentage we would have to invent.
type InstallStatus struct {
	Serial   string
	State    InstallState
	Err      error
	Output   string
	Started  time.Time
	Finished time.Time
}

// Elapsed is how long this device has been installing, or took.
func (s InstallStatus) Elapsed() time.Duration {
	if s.Started.IsZero() {
		return 0
	}
	if s.Finished.IsZero() {
		return time.Since(s.Started)
	}
	return s.Finished.Sub(s.Started)
}

// InstallRun is a fan-out install in flight. One device failing never stops
// the others: a fleet install is only useful if it tells you which devices
// worked, so every target runs to a terminal state and failures are collected.
type InstallRun struct {
	APK     string
	updates chan InstallStatus
	cancel  context.CancelFunc

	mu       sync.Mutex
	statuses map[string]InstallStatus
	order    []string
}

// Updates yields every state transition. It is closed once every device has
// reached a terminal state.
func (r *InstallRun) Updates() <-chan InstallStatus { return r.updates }

// Cancel stops queued and in-flight installs. Devices that already finished
// keep their result; a device killed mid-install reports InstallCancelled,
// which does NOT mean the package is absent — adb may have committed the
// session before the process died.
func (r *InstallRun) Cancel() { r.cancel() }

// Statuses returns every device's current status, in target order.
func (r *InstallRun) Statuses() []InstallStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]InstallStatus, 0, len(r.order))
	for _, serial := range r.order {
		out = append(out, r.statuses[serial])
	}
	return out
}

// Tally counts devices by outcome.
func (r *InstallRun) Tally() (done, failed, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.statuses {
		total++
		if s.State.Done() {
			done++
		}
		if s.State == InstallFailed || s.State == InstallCancelled {
			failed++
		}
	}
	return done, failed, total
}

func (r *InstallRun) set(status InstallStatus) {
	r.mu.Lock()
	r.statuses[status.Serial] = status
	r.mu.Unlock()
	// Updates is buffered to hold every device's full transition sequence,
	// so this never blocks a worker.
	r.updates <- status
}

// Install runs `adb install` against every serial in parallel. concurrency
// caps simultaneous installs (<= 0 means unlimited); a shared USB hub can be
// the bottleneck, so this is worth turning down on a big fleet.
func (f *Fleet) Install(ctx context.Context, apk string, serials []string, opts adb.InstallOptions, concurrency int) (*InstallRun, error) {
	if len(serials) == 0 {
		return nil, errors.New("no devices selected")
	}
	info, err := os.Stat(apk)
	if err != nil {
		return nil, fmt.Errorf("apk: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("apk: %s is a directory", apk)
	}

	targets := append([]string(nil), serials...)
	sort.Strings(targets)

	runCtx, cancel := context.WithCancel(ctx)
	run := &InstallRun{
		APK: apk,
		// 4 transitions per device is more than the 2 we actually emit,
		// leaving headroom so set() can never block.
		updates:  make(chan InstallStatus, len(targets)*4),
		cancel:   cancel,
		statuses: make(map[string]InstallStatus, len(targets)),
		order:    targets,
	}
	for _, serial := range targets {
		run.statuses[serial] = InstallStatus{Serial: serial, State: InstallPending}
	}

	if concurrency <= 0 || concurrency > len(targets) {
		concurrency = len(targets)
	}
	slots := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
	for _, serial := range targets {
		wg.Add(1)
		go func(serial string) {
			defer wg.Done()

			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-runCtx.Done():
				run.set(InstallStatus{Serial: serial, State: InstallCancelled, Err: runCtx.Err()})
				return
			}

			started := time.Now()
			run.set(InstallStatus{Serial: serial, State: InstallRunning, Started: started})

			output, err := f.client.Install(runCtx, serial, apk, opts)
			status := InstallStatus{
				Serial:   serial,
				Output:   output,
				Started:  started,
				Finished: time.Now(),
			}
			switch {
			case runCtx.Err() != nil:
				status.State = InstallCancelled
				status.Err = runCtx.Err()
			case err != nil:
				status.State = InstallFailed
				status.Err = err
			default:
				status.State = InstallSucceeded
			}
			run.set(status)
		}(serial)
	}

	go func() {
		wg.Wait()
		cancel()
		close(run.updates)
	}()

	return run, nil
}
