// Package fleet tracks every attached device at once and merges their logcat
// output into a single stream. This is the part that makes lazyadb a fleet
// tool rather than another single-device dashboard: tails are started and
// stopped as devices come and go, and every line carries the serial it came
// from so the UI can render a device column.
package fleet

import (
	"context"
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
	Text   string
	At     time.Time
}

// Fleet owns one logcat tail per online device.
type Fleet struct {
	client *adb.Client
	lines  chan Line

	mu      sync.Mutex
	devices []adb.Device
	tails   map[string]context.CancelFunc
	dropped int
}

// New returns a Fleet that buffers up to buffer merged lines.
func New(client *adb.Client, buffer int) *Fleet {
	if buffer <= 0 {
		buffer = 1024
	}
	return &Fleet{
		client: client,
		lines:  make(chan Line, buffer),
		tails:  make(map[string]context.CancelFunc),
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
	for serial, cancel := range f.tails {
		if !online[serial] {
			cancel()
			delete(f.tails, serial)
		}
	}
	for serial := range online {
		if _, tailing := f.tails[serial]; !tailing {
			tailCtx, cancel := context.WithCancel(ctx)
			f.tails[serial] = cancel
			go f.tail(tailCtx, serial)
		}
	}
	f.mu.Unlock()

	return found, nil
}

// Close stops every running tail.
func (f *Fleet) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for serial, cancel := range f.tails {
		cancel()
		delete(f.tails, serial)
	}
}

func (f *Fleet) tail(ctx context.Context, serial string) {
	stream, err := f.client.Logcat(ctx, serial)
	if err != nil {
		f.emit(Line{Serial: serial, Level: 'E', Tag: "lazyadb", Text: err.Error(), At: time.Now()})
		return
	}
	defer stream.Close()

	go func() {
		<-ctx.Done()
		stream.Close()
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
// Anything that doesn't match is kept verbatim rather than dropped.
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
	tag, _, ok := strings.Cut(head, "(")
	if !ok {
		tag = head
	}
	line.Tag = strings.TrimSpace(tag)
	line.Text = msg
	return line
}
