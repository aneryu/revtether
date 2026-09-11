//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"
	"revtether/internal/relay"
	"revtether/internal/ui"
)

const (
	appName        = "Reverse Tether"
	maxDeviceSlots = 4
)

var version = "dev"

func main() {
	lock, err := acquireSingleton()
	if err != nil {
		os.Exit(0)
	}
	defer lock.Close()
	augmentPATH()
	systray.Run(onReady, nil)
}

func onReady() {
	hideDock()
	icon := menuBarIconPNG()
	systray.SetTemplateIcon(icon, icon)
	systray.SetTooltip(appName)

	logPath, logFile, err := openLogFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "log file: %v\n", err)
		logFile = os.Stderr
		logPath = ""
	}

	log := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	log.Info("reverse tether starting", "version", version, "path", os.Getenv("PATH"))

	fe := relay.NewFrontend(io.Discard)
	disp := fe.Display
	ctx, cancel := context.WithCancel(context.Background())
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			systray.Quit()
		})
	}

	empty := systray.AddMenuItem("未连接 USB", "")
	source := systray.AddMenuItem("状态来自终端", "")
	source.Hide()
	slots := make([]*systray.MenuItem, maxDeviceSlots)
	for i := range slots {
		item := systray.AddMenuItemCheckbox("", "", true)
		item.Hide()
		slots[i] = item
	}
	overflow := systray.AddMenuItem("", "")
	overflow.Hide()

	systray.AddSeparator()
	openLog := systray.AddMenuItem("打开日志", "")
	if logPath == "" {
		openLog.Disable()
	}
	mQuit := systray.AddMenuItem("退出", "")

	var viewsMu sync.Mutex
	var views []ui.DeviceView

	refresh := func() {
		cur := disp.Snapshot()
		viewsMu.Lock()
		views = cur
		viewsMu.Unlock()

		if len(cur) == 0 {
			if fe.Viewing() {
				empty.SetTitle("状态来自终端 · 未连接 USB")
			} else {
				empty.SetTitle("未连接 USB")
			}
			systray.SetTooltip(appName)
			empty.Show()
			source.Hide()
		} else {
			n := 0
			for _, v := range cur {
				if v.State == ui.StateConnected && fe.Enabled(v.ID) {
					n++
				}
			}
			if n > 0 {
				systray.SetTooltip(fmt.Sprintf("%s — %d 台已连接", appName, n))
			} else {
				systray.SetTooltip(appName)
			}
			empty.Hide()
			if fe.Viewing() {
				source.SetTitle("状态来自终端")
				source.Show()
			} else {
				source.Hide()
			}
		}

		shown := cur
		if len(shown) > len(slots) {
			shown = cur[:len(slots)]
		}
		for i, item := range slots {
			if i < len(shown) {
				v := shown[i]
				item.SetTitle(deviceTitle(v))
				item.SetTooltip(v.Serial)
				on := fe.Enabled(v.ID)
				if on != item.Checked() {
					if on {
						item.Check()
					} else {
						item.Uncheck()
					}
				}
				item.Show()
			} else {
				item.Hide()
			}
		}
		if len(cur) > len(slots) {
			overflow.SetTitle(fmt.Sprintf("还有 %d 台", len(cur)-len(slots)))
			overflow.Show()
		} else {
			overflow.Hide()
		}
	}

	go func() {
		if err := relay.RunShared(ctx, relay.Config{Log: log, Display: disp, Frontend: fe, Kind: "app"}); err != nil {
			log.Error("relay stopped", "err", err)
			disp.Logf("relay: %v", err)
		}
	}()

	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		refresh()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				refresh()
			}
		}
	}()

	for i := range slots {
		i := i
		go func() {
			for range slots[i].ClickedCh {
				viewsMu.Lock()
				cur := views
				viewsMu.Unlock()
				if i >= len(cur) {
					continue
				}
				id := cur[i].ID
				on := !fe.Enabled(id)
				fe.SetEnabled(id, on)
				if on {
					slots[i].Check()
					log.Info("device enabled", "id", id, "serial", cur[i].Serial)
				} else {
					slots[i].Uncheck()
					log.Info("device disabled", "id", id, "serial", cur[i].Serial)
				}
			}
		}()
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-mQuit.ClickedCh:
				log.Info("quit from menu")
				stop()
				return
			case <-openLog.ClickedCh:
				if logPath != "" {
					_ = exec.Command("open", logPath).Start()
				}
			}
		}
	}()
}

func openLogFile() (string, *os.File, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(home, "Library", "Logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "ReverseTether.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", nil, err
	}
	return path, f, nil
}
