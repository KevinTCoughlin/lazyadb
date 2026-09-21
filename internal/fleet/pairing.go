package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
)

// PairResult describes how a pair-and-connect attempt went.
type PairResult struct {
	PairAddr    string
	ConnectAddr string
	Connected   bool
	Output      string
}

// connectPollInterval is how often we re-query mDNS while waiting for a
// freshly paired device to start advertising its connect service.
const connectPollInterval = 500 * time.Millisecond

// Discover lists devices on the network that are either waiting to be paired
// or already paired and connectable.
func (f *Fleet) Discover(ctx context.Context) ([]adb.MDNSService, error) {
	return f.client.MDNSServices(ctx)
}

// Connect attaches to an already-paired device at host:port.
func (f *Fleet) Connect(ctx context.Context, addr string) (string, error) {
	return f.client.Connect(ctx, addr)
}

// PairAndConnect runs the whole wireless handshake as one step: pair against
// the pairing port, then find the device's *connect* port and attach to it.
//
// Doing this by hand is the worst part of wireless debugging — the pairing
// port shown on screen is not the port you connect to, so you pair, then go
// dig the other port out of `adb mdns services` yourself. Here the device ID
// in the mDNS name links the two.
//
// waitForConnect bounds how long to wait for the connect service to appear;
// devices typically take a second or two after pairing.
func (f *Fleet) PairAndConnect(ctx context.Context, pairAddr, code string, waitForConnect time.Duration) (PairResult, error) {
	result := PairResult{PairAddr: pairAddr}

	code = strings.TrimSpace(code)
	if code == "" {
		return result, fmt.Errorf("pairing code is required")
	}

	output, err := f.client.Pair(ctx, pairAddr, code)
	result.Output = output
	if err != nil {
		return result, err
	}

	// Pairing succeeded. Anything that fails from here is a connect problem,
	// not a pairing problem, and must be reported as such — the user should
	// not be sent back to re-enter a code that already worked.
	addr, err := f.resolveConnectAddr(ctx, pairAddr, waitForConnect)
	if err != nil {
		return result, fmt.Errorf("paired, but %w", err)
	}
	result.ConnectAddr = addr

	connectOut, err := f.client.Connect(ctx, addr)
	if connectOut != "" {
		result.Output += "\n" + connectOut
	}
	if err != nil {
		return result, fmt.Errorf("paired, but connect to %s failed: %w", addr, err)
	}

	result.Connected = true
	return result, nil
}

// resolveConnectAddr finds the connect endpoint for the device we just paired
// with, polling until it advertises one.
func (f *Fleet) resolveConnectAddr(ctx context.Context, pairAddr string, wait time.Duration) (string, error) {
	pairHost := hostOf(pairAddr)
	deadline := time.Now().Add(wait)

	var wantID string
	for {
		services, err := f.client.MDNSServices(ctx)
		if err == nil {
			// Learn the device ID from the pairing service while it is still
			// advertised; it disappears shortly after a successful pair.
			if wantID == "" {
				for _, s := range services {
					if s.Pairable() && s.Addr == pairAddr {
						wantID = s.DeviceID()
						break
					}
				}
			}
			if addr := matchConnect(services, wantID, pairHost); addr != "" {
				return addr, nil
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("no connect service appeared within %s: connect manually with `adb connect %s:<port>`", wait, pairHost)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(connectPollInterval):
		}
	}
}

// matchConnect prefers the device ID we paired with, and falls back to any
// connectable service on the same host — some adb builds rotate the mDNS
// instance name after pairing.
func matchConnect(services []adb.MDNSService, wantID, host string) string {
	if wantID != "" {
		for _, s := range services {
			if s.Connectable() && s.DeviceID() == wantID {
				return s.Addr
			}
		}
	}
	for _, s := range services {
		if s.Connectable() && hostOf(s.Addr) == host {
			return s.Addr
		}
	}
	return ""
}

func hostOf(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
