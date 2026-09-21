package adb

import (
	"context"
	"fmt"
	"strings"
)

// mDNS service types adb advertises. A device in "pair with code" mode
// advertises the pairing service; once paired and reachable it advertises the
// connect service. The two live on different ports, which is the whole
// awkwardness of wireless debugging by hand: you pair against one and then
// have to go find the other.
const (
	ServicePairing = "_adb-tls-pairing._tcp"
	ServiceConnect = "_adb-tls-connect._tcp"
	ServiceLegacy  = "_adb._tcp"
)

// MDNSService is one row of `adb mdns services`.
type MDNSService struct {
	Name string // e.g. adb-39121FDJG004TC-vWgJpq
	Type string
	Addr string // host:port
}

// Pairable reports whether this service is a device waiting for a pairing code.
func (s MDNSService) Pairable() bool { return s.Type == ServicePairing }

// Connectable reports whether this service is a paired device ready to connect.
func (s MDNSService) Connectable() bool {
	return s.Type == ServiceConnect || s.Type == ServiceLegacy
}

// DeviceID is the stable middle part of an adb mDNS name — the same device
// advertises its pairing and connect services under the same ID, which is how
// we match one to the other after a successful pair.
func (s MDNSService) DeviceID() string {
	parts := strings.Split(s.Name, "-")
	if len(parts) >= 2 {
		return parts[1]
	}
	return s.Name
}

// MDNSServices lists devices adb has discovered on the local network.
func (c *Client) MDNSServices(ctx context.Context) ([]MDNSService, error) {
	out, err := c.output(ctx, "mdns", "services")
	if err != nil {
		return nil, fmt.Errorf("adb mdns services: %w", err)
	}
	return parseMDNSServices(out), nil
}

func parseMDNSServices(out string) []MDNSService {
	var services []MDNSService
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of discovered") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		svc := MDNSService{Name: fields[0], Type: fields[1], Addr: fields[2]}
		if svc.Addr == "" || !strings.Contains(svc.Addr, ":") {
			continue
		}
		services = append(services, svc)
	}
	return services
}

// Pair completes the pairing handshake with a device in pairing mode. addr is
// the host:port shown on the device (or discovered via mDNS) and code is the
// six-digit pairing code it displays.
func (c *Client) Pair(ctx context.Context, addr, code string) (string, error) {
	out, err := c.output(ctx, "pair", addr, code)
	output := strings.TrimSpace(out)
	if err != nil {
		if output != "" {
			return output, fmt.Errorf("%s", lastLine(output))
		}
		return output, err
	}
	// adb pair exits 0 even when the code is wrong, so the output is the only
	// signal that actually distinguishes success from failure.
	if !strings.Contains(output, "Successfully paired") {
		if output == "" {
			output = "pairing failed"
		}
		return output, fmt.Errorf("%s", lastLine(output))
	}
	return output, nil
}

// Connect attaches to a paired device at host:port.
func (c *Client) Connect(ctx context.Context, addr string) (string, error) {
	out, err := c.output(ctx, "connect", addr)
	output := strings.TrimSpace(out)
	if err != nil {
		if output != "" {
			return output, fmt.Errorf("%s", lastLine(output))
		}
		return output, err
	}
	// Same story as pair: `adb connect` reports "failed to connect" on stdout
	// with a zero exit status.
	if strings.Contains(output, "failed to connect") || strings.Contains(output, "cannot connect") {
		return output, fmt.Errorf("%s", lastLine(output))
	}
	return output, nil
}

// Disconnect detaches a wireless device.
func (c *Client) Disconnect(ctx context.Context, addr string) error {
	_, err := c.output(ctx, "disconnect", addr)
	return err
}
