// Package adb is a thin, dumb wrapper around the adb binary. It knows how to
// enumerate devices and open a logcat stream; it holds no state about the
// fleet and makes no decisions about what to do with a device.
package adb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// DefaultBinary is used when no explicit adb path is configured.
const DefaultBinary = "adb"

// Client runs adb subcommands.
type Client struct {
	Binary string
}

// New returns a Client bound to binary, or to adb on PATH when binary is empty.
func New(binary string) *Client {
	if binary == "" {
		binary = DefaultBinary
	}
	return &Client{Binary: binary}
}

// Device is one entry from `adb devices -l`.
type Device struct {
	Serial      string
	State       string
	Model       string
	Product     string
	Device      string
	TransportID string
}

// Online reports whether the device is in a state we can talk to. Devices that
// are unauthorized, offline, or still booting are listed but not tailable.
func (d Device) Online() bool { return d.State == "device" }

// Label is the friendliest name we have for the device.
func (d Device) Label() string {
	if d.Model != "" {
		return strings.ReplaceAll(d.Model, "_", " ")
	}
	return d.Serial
}

// Ping verifies the adb binary is usable, starting the server if needed.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := exec.LookPath(c.Binary); err != nil {
		return fmt.Errorf("adb not found (looked for %q): install the Android platform-tools or pass -adb", c.Binary)
	}
	if _, err := c.output(ctx, "start-server"); err != nil {
		return fmt.Errorf("adb start-server: %w", err)
	}
	return nil
}

// Devices returns every device adb currently knows about, online or not.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	out, err := c.output(ctx, "devices", "-l")
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	return parseDevices(out), nil
}

func parseDevices(out string) []Device {
	var devices []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := Device{Serial: fields[0], State: fields[1]}
		for _, kv := range fields[2:] {
			key, value, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch key {
			case "model":
				d.Model = value
			case "product":
				d.Product = value
			case "device":
				d.Device = value
			case "transport_id":
				d.TransportID = value
			}
		}
		devices = append(devices, d)
	}
	return devices
}

// InstallOptions tunes a single `adb install`.
type InstallOptions struct {
	// Reinstall passes -r, keeping app data. This is the normal case in a
	// dev loop; without it adb fails outright when the package is present.
	Reinstall bool
	// ExtraArgs are passed through verbatim before the apk path, e.g. -g to
	// grant runtime permissions or -d to allow a downgrade.
	ExtraArgs []string
}

// Install pushes apk to serial. It returns adb's combined output either way,
// so the caller can show the real failure reason rather than "exit status 1".
func (c *Client) Install(ctx context.Context, serial, apk string, opts InstallOptions) (string, error) {
	args := []string{"-s", serial, "install"}
	if opts.Reinstall {
		args = append(args, "-r")
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, apk)

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	raw, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(raw))
	return output, installError(output, err)
}

// installError normalizes adb's two failure conventions. Older adb exits 0
// and prints "Failure [INSTALL_FAILED_...]"; newer adb exits non-zero. Both
// must be treated as failures, or a fleet install reports false success.
func installError(output string, err error) error {
	if failure := failureReason(output); failure != "" {
		return fmt.Errorf("%s", failure)
	}
	if err != nil {
		if output != "" {
			return fmt.Errorf("%w: %s", err, lastLine(output))
		}
		return err
	}
	return nil
}

func failureReason(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Failure") || strings.HasPrefix(line, "Error:") {
			return line
		}
		if strings.HasPrefix(line, "adb: failed to install") {
			return strings.TrimPrefix(line, "adb: ")
		}
	}
	return ""
}

func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Stream is a running `adb logcat` against one device.
type Stream struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser

	once sync.Once
}

// Logcat starts tailing logcat on serial. The caller owns the returned Stream
// and must Close it; cancelling ctx also tears the subprocess down.
func (c *Client) Logcat(ctx context.Context, serial string, extra ...string) (*Stream, error) {
	args := append([]string{"-s", serial, "logcat", "-v", "brief"}, extra...)
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// logcat is chatty on stderr about buffer switches; we don't want it
	// interleaved into the fleet view.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("adb -s %s logcat: %w", serial, err)
	}
	return &Stream{cmd: cmd, stdout: stdout}, nil
}

// Scan calls fn for every line until the stream ends or is closed. It returns
// nil on a clean end-of-stream, including the case where Close killed the
// subprocess.
func (s *Stream) Scan(fn func(string)) error {
	scanner := bufio.NewScanner(s.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fn(scanner.Text())
	}
	err := scanner.Err()
	waitErr := s.cmd.Wait()
	if err != nil {
		return err
	}
	// A killed subprocess is the normal shutdown path, not a failure.
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil
		}
		return waitErr
	}
	return nil
}

// Close stops the underlying adb process. It is safe to call more than once.
func (s *Stream) Close() error {
	s.once.Do(func() {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.stdout.Close()
	})
	return nil
}

func (c *Client) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}
