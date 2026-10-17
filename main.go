package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/coalaura/plain"
	"github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend"
)

var log = plain.New(plain.WithDate(plain.RFC3339Local))

func main() {
	directory, err := os.UserConfigDir()
	if err != nil {
		directory = os.TempDir()
	}

	path := filepath.Join(directory, "netmon", "logs", "netmon-"+time.Now().Format("20060102-150405.000000000")+".csv")
	monitor := &monitor{logPath: path, done: make(chan struct{})}

	writer, err := openCSV(path)
	if err != nil {
		monitor.logError = err.Error()

		log.Errorf("Cannot open CSV %s: %v\n", path, err)
	} else {
		log.Printf("Logging samples to %s\n", path)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dashboard := newDashboard(monitor)

	gui.SetTheme(gui.ThemeDark)

	window := gui.NewWindow(gui.WindowCfg{
		Title:     "Netmon",
		Width:     1180,
		Height:    740,
		MinWidth:  900,
		MinHeight: 620,
		BgColor:   colorBackground,
		OnInit: func(window *gui.Window) {
			window.SetView(dashboard.view)
		},
		OnCloseRequest: func(window *gui.Window) {
			cancel()
			window.Close()
		},
	})

	var refreshQueued atomic.Bool

	go monitor.run(ctx, writer, func() {
		// Coalesce refreshes while the window is minimized or its event loop is busy.
		if !refreshQueued.CompareAndSwap(false, true) {
			return
		}

		window.QueueCommand(func(window *gui.Window) {
			refreshQueued.Store(false)

			if dashboard.frozen {
				return
			}

			dashboard.refresh()
			window.InvalidateLayout()
		})
	})

	backend.Run(window)

	cancel()
	<-monitor.done

	log.Info("Monitoring stopped")
}
