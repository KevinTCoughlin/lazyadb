# lazyadb

A lazydocker-style terminal UI for a **fleet** of Android devices — every
attached phone, tablet, emulator, and wireless target in one keyboard-driven
panel, with their logcat streams merged into a single tagged view.

## Why another adb TUI

There are already several good single-device adb TUIs. Every one of them
rebuilds the same dashboard: pick a device, then look at its logcat, its
shell, its files. Above them sits a cliff — self-hosted device-farm web apps
(STF/Device Farmer) and paid SaaS device clouds.

The gap is in between: *"I have six devices on a USB hub and over wireless
debugging, and I want one panel."* lazyadb is fleet-first. The device set is
the primary object, not a dropdown you choose from before the real UI starts.

## Status

Early. What works today:

- **Fleet discovery** — continuous `adb devices -l` polling; devices appearing
  and disappearing are picked up without a restart. USB and wireless
  (`host:port`) targets are treated identically.
- **Merged logcat** — one tail per online device, interleaved into a single
  stream with a per-device color-coded column, so you can watch the whole
  fleet react to the same action at once.
- **Scope toggle** — `a` flips between the whole fleet and just the selected
  device, without dropping the other tails.
- **Filter, pause, clear** — substring filter across message and tag, pause to
  read, clear to reset.
- **Wireless pairing** — `w` opens a pairing panel that discovers devices over
  mDNS, takes the pairing code, and then *finds the connect port itself*. The
  port a device shows you for pairing is not the port you connect on, so by
  hand this means pairing and then digging the other port out of `adb mdns
  services`. Manual address entry is there too, because mDNS does not survive
  most corporate wifi.
- **Reattaching to known devices** — paired devices are remembered and
  reconnected at startup. Pairing survives a reboot but the *connection* does
  not, and the connect port is dynamic, so reattaching follows the device's
  mDNS identity to wherever it is now rather than retrying a saved address
  that has usually gone stale. The saved address is kept as the fallback for
  when mDNS is unavailable, and is rewritten whenever a device turns up
  somewhere new.
- **Parallel install** — `i` installs an APK to every online device at once,
  with per-device results. One device failing never aborts the others.

Devices that are `offline` or `unauthorized` are listed with their state but
not tailed.

## Install

```bash
go install github.com/kevintcoughlin/lazyadb/cmd/lazyadb@latest
```

Requires Go 1.25+ and `adb` (Android platform-tools) on your `PATH`.

## Usage

```bash
lazyadb                      # tail every attached device
lazyadb -adb /opt/adb        # use a specific adb binary
lazyadb -poll 5s             # re-scan for devices less aggressively
```

| Key | Action |
| --- | --- |
| `j` / `k` | move through the device list |
| `a` | toggle between the whole fleet and the selected device |
| `w` | open the wireless pairing panel |
| `i` | install an APK across the current scope |
| `/` | filter by substring (message or tag); `enter` applies, `esc` clears |
| `p` / `space` | pause the log |
| `c` | clear the buffer |
| `r` | rescan for devices now |
| `q` | quit |

## Architecture

```
cmd/lazyadb      entrypoint and flags
internal/adb     thin wrapper around the adb binary; no fleet state
internal/fleet   device set + one logcat tail per device, merged into one channel
internal/known   remembered wireless devices, persisted as JSON
internal/ui      Bubble Tea front end
```

`internal/adb` deliberately knows nothing about the fleet, so it stays easy to
test and to swap for a pure-Go adb client later. `internal/fleet` owns all the
lifecycle: starting a tail when a device appears, cancelling it when the device
goes away, and dropping lines rather than blocking when a busy fleet
out-produces the UI (the drop count is shown in the status bar).

## Roadmap

Honest framing: merged logcat and wireless pairing are the parts that earn
their keep, because they are genuinely awkward to do in a shell loop. Fan-out
writes are convenient, but `for s in $(adb devices ...)` is a real competitor
there, so the per-device *reporting* is what matters, not the fan-out itself.

- fan-out `shell`, with output collated per device
- `getprop` diff across devices (API level, ABI, build fingerprint)
- device groups and saved selections
- MCP server so an agent can drive the fleet

## License

MIT — see [LICENSE](LICENSE).
