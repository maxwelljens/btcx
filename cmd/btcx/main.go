// Command btcx is a lean, modern mail user agent for Linux.
//
// Usage:
//
//	btcx [-config dir] [-debug] [-version]
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"btcx/internal/app"
	"btcx/internal/buildinfo"
	"btcx/internal/config"
	"btcx/internal/ui"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		configDir   string
		showVersion bool
		debug       bool
	)
	flag.StringVar(&configDir, "config", "", "configuration directory (default: ~/.config/btcx)")
	flag.BoolVar(&showVersion, "version", false, "print version information and exit")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.Parse()

	if showVersion {
		fmt.Println("btcx", buildinfo.String())
		return 0
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	dir := configDir
	if dir == "" {
		d, err := config.DefaultDir()
		if err != nil {
			logger.Error("cannot determine config directory", "err", err)
			return 1
		}
		dir = d
	}

	// First launch: generate commented example configuration before
	// loading, so a new user always has a working, explained skeleton.
	var generated []string
	firstRun := false
	if fresh, err := config.FirstRun(dir); err != nil {
		logger.Warn("cannot inspect config directory", "dir", dir, "err", err)
	} else if fresh {
		created, err := config.Bootstrap(dir)
		if err != nil {
			logger.Warn("cannot write example configuration", "dir", dir, "err", err)
		} else {
			firstRun = true
			generated = created
			for _, f := range created {
				logger.Info("created example configuration", "file", filepath.Join(dir, f))
			}
		}
	}

	cfg, warnings, err := config.LoadDir(dir)
	if err != nil {
		logger.Error("configuration error", "dir", dir, "err", err)
		return 1
	}
	for _, w := range warnings {
		logger.Warn("configuration warning", "file", w.File, "key", w.Key, "warning", w.Message)
	}

	// The UI attaches its event sink via SetEvents before calling Start.
	controller := app.New(cfg, nil, logger, nil)

	opts := ui.Options{
		Title:          "btcx",
		Theme:          cfg.Theme,
		Version:        buildinfo.Version(),
		ConfigDir:      dir,
		FirstRun:       firstRun,
		GeneratedFiles: generated,
	}
	// ui.Run owns the GTK main loop and the controller lifecycle
	// (Start on activation, Shutdown on quit).
	code := ui.Run(opts, controller, []string{os.Args[0]})
	return code
}
