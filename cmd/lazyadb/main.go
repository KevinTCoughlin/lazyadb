// Command lazyadb is a terminal UI for working with a whole fleet of Android
// devices at once.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kevintcoughlin/lazyadb/internal/adb"
	"github.com/kevintcoughlin/lazyadb/internal/fleet"
	"github.com/kevintcoughlin/lazyadb/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lazyadb:", err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("adb", "", "path to the adb binary (default: adb on PATH)")
	poll := flag.Duration("poll", 2*time.Second, "how often to re-scan for attached devices")
	buffer := flag.Int("buffer", 4096, "merged log channel buffer; lines are dropped when the UI falls behind")
	reinstall := flag.Bool("reinstall", true, "pass -r when installing, keeping app data")
	installArgs := flag.String("install-args", "", "extra space-separated flags for adb install, e.g. \"-g -d\"")
	jobs := flag.Int("jobs", 0, "max simultaneous installs; 0 means one per device")
	flag.Parse()

	client := adb.New(*binary)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		return err
	}

	f := fleet.New(client, *buffer)
	defer f.Close()

	opts := adb.InstallOptions{
		Reinstall: *reinstall,
		ExtraArgs: strings.Fields(*installArgs),
	}

	program := tea.NewProgram(ui.New(ctx, f, *poll, opts, *jobs), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return err
	}
	return nil
}
