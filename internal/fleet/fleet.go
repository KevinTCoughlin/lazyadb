// Package fleet tracks every attached device at once and merges their logcat
// output into a single stream. This is the part that makes lazyadb a fleet
// tool rather than another single-device dashboard: tails are started and
// stopped as devices come and go, and every line carries the serial it came
// from so the UI can render a device column.
package fleet

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
)

// Line is one logcat line, tagged with its origin.
type Line struct {
	Serial string
	Level  byte // V, D, I, W, E, F, or 0 when unparsed
	Tag    string
	// PID is the process that logged the line, or 0 when the line carried
	// none. It is the device's PID: the same number on two devices is two
	// unrelated processes.
	PID  int
	Text string
	At   time.Time
}

// firstAttachBacklog is how many retained lines the first tail of a device
// replays, so opening lazyadb isn't an empty screen. Later tails of the same
// serial start at the newest line instead: wireless devices drop and return
// routinely, and replaying the whole ring buffer on every return floods the
// merged view and evicts the other devices' recent lines from the log buffer.
const firstAttachBacklog = 200

// Fleet owns one logcat tail per online device.
type Fleet struct {
	client *adb.Client
	lines  chan Line

	mu      sync.Mutex
	devices []adb.Device
	tails   map[string]*tailHandle
	// attached holds every serial that has been tailed this session, so a
	// reattach can be told apart from a first attach.
	attached map[string]bool
	dropped  int
}

// tailHandle identifies one running tail, so a tail that ends on its own can
// remove itself from the map without clobbering a newer tail for the same
// serial.
type tailHandle struct {
	cancel context.CancelFunc
}

// New returns a Fleet that buffers up to buffer merged lines.
func New(client *adb.Client, buffer int) *Fleet {
	if buffer <= 0 {
		buffer = 1024
	}
	return &Fleet{
		client:   client,
		lines:    make(chan Line, buffer),
		tails:    make(map[string]*tailHandle),
		attached: make(map[string]bool),
	}
}

// Lines is the merged stream across every tailed device.
func (f *Fleet) Lines() <-chan Line { return f.lines }

// Devices returns the last known device set.
func (f *Fleet) Devices() []adb.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adb.Device(nil), f.devices...)
}

// Dropped is the number of lines discarded because the consumer fell behind.
// A busy fleet can out-produce the UI, and dropping is better than blocking
// the tail goroutines.
func (f *Fleet) Dropped() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

// Refresh re-enumerates devices, starting tails for ones that appeared and
// cancelling tails for ones that went away. ctx governs the tails it starts,
// so cancelling it shuts the whole fleet down.
func (f *Fleet) Refresh(ctx context.Context) ([]adb.Device, error) {
	found, err := f.client.Devices(ctx)
	if err != nil {
		return nil, err
	}

	online := make(map[string]bool, len(found))
	for _, d := range found {
		if d.Online() {
			online[d.Serial] = true
		}
	}

	f.mu.Lock()
	f.devices = found
	for serial, h := range f.tails {
		if !online[serial] {
			h.cancel()
			delete(f.tails, serial)
		}
	}
	for serial := range online {
		if _, tailing := f.tails[serial]; !tailing {
			tailCtx, cancel := context.WithCancel(ctx)
			h := &tailHandle{cancel: cancel}
			f.tails[serial] = h
			go f.tail(tailCtx, h, serial, f.logcatArgs(serial))
		}
	}
	f.mu.Unlock()

	return found, nil
}

// Close stops every running tail.
func (f *Fleet) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for serial, h := range f.tails {
		h.cancel()
		delete(f.tails, serial)
	}
}

// logcatArgs picks where a new tail of serial starts. f.mu must be held.
func (f *Fleet) logcatArgs(serial string) []string {
	if f.attached[serial] {
		return []string{"-T", "1"}
	}
	f.attached[serial] = true
	return []string{"-T", strconv.Itoa(firstAttachBacklog)}
}

func (f *Fleet) tail(ctx context.Context, h *tailHandle, serial string, args []string) {
	// adb's logcat exits when the device drops. If the device is back before
	// the next Refresh notices it was gone, Refresh still sees this tail in
	// the map and would never start another, so a tail that ends for any
	// reason removes itself and the next Refresh reattaches.
	defer func() {
		h.cancel()
		f.mu.Lock()
		if f.tails[serial] == h {
			delete(f.tails, serial)
		}
		f.mu.Unlock()
	}()

	stream, err := f.client.Logcat(ctx, serial, args...)
	if err != nil {
		f.emit(Line{Serial: serial, Level: 'E', Tag: "lazyadb", Text: err.Error(), At: time.Now()})
		return
	}
	// Closing is how the tail is torn down, from either side; by then there
	// is nothing useful to do with a close error.
	defer func() { _ = stream.Close() }()

	go func() {
		<-ctx.Done()
		_ = stream.Close()
	}()

	_ = stream.Scan(func(raw string) {
		f.emit(parseLine(serial, raw))
	})
}

func (f *Fleet) emit(line Line) {
	select {
	case f.lines <- line:
	default:
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
	}
}

// parseLine understands logcat's "brief" format: "I/Tag( 1234): message".
// logcat pads the PID to five columns, hence the space. Anything that doesn't
// match is kept verbatim rather than dropped.
func parseLine(serial, raw string) Line {
	line := Line{Serial: serial, Text: raw, At: time.Now()}
	if len(raw) < 2 || raw[1] != '/' {
		return line
	}
	switch raw[0] {
	case 'V', 'D', 'I', 'W', 'E', 'F':
		line.Level = raw[0]
	default:
		return line
	}
	rest := raw[2:]
	head, msg, ok := strings.Cut(rest, ": ")
	if !ok {
		return line
	}
	tag := head
	// The PID group is the last parenthesised part, so a tag that itself
	// contains "(" still splits correctly.
	if open := strings.LastIndexByte(head, '('); open != -1 {
		tag = head[:open]
		if pid, ok := strings.CutSuffix(head[open+1:], ")"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(pid)); err == nil && n > 0 {
				line.PID = n
			}
		}
	}
	line.Tag = strings.TrimSpace(tag)
	line.Text = msg
	return line
}
