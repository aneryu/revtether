//go:build unix

package relay

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"revtether/internal/ui"
)

// Frontend is the UI-facing handle used by both the CLI and the menu bar app.
// One process owns the relay; the other mirrors connection state over the hub.
type Frontend struct {
	Display *ui.Display
	ctrl    *Control

	mu     sync.Mutex
	client *HubClient
	kind   string
}

func NewFrontend(out io.Writer) *Frontend {
	return &Frontend{
		Display: ui.New(out),
		ctrl:    NewControl(),
	}
}

func (f *Frontend) Enabled(id string) bool {
	if f.Viewing() {
		for _, v := range f.Display.Snapshot() {
			if v.ID == id {
				return v.Enabled
			}
		}
		return true
	}
	return f.ctrl.Enabled(id)
}

func (f *Frontend) SetEnabled(id string, on bool) {
	f.mu.Lock()
	c := f.client
	f.mu.Unlock()
	if c != nil {
		f.Display.PatchEnabled(id, on)
		_ = c.SetEnabled(id, on)
		return
	}
	f.ctrl.SetEnabled(id, on)
}

func (f *Frontend) Viewing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.client != nil
}

func (f *Frontend) SourceKind() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kind
}

func (f *Frontend) setClient(c *HubClient, kind string) {
	f.mu.Lock()
	f.client = c
	f.kind = kind
	f.mu.Unlock()
}

// RunShared runs the relay when this process can take ownership, otherwise
// mirrors the owner's connection status until that process exits.
func RunShared(ctx context.Context, cfg Config) error {
	if err := cfg.prepareCapture(); err != nil {
		return err
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Frontend == nil {
		fe := NewFrontend(io.Discard)
		if cfg.Display != nil {
			fe.Display = cfg.Display
		}
		if cfg.Control != nil {
			fe.ctrl = cfg.Control
		}
		cfg.Frontend = fe
	}
	cfg.Display = cfg.Frontend.Display
	cfg.Control = cfg.Frontend.ctrl
	if cfg.Kind == "" {
		cfg.Kind = kindCLI
	}

	dir := cfg.Dir
	if dir == "" {
		var err error
		dir, err = SupportDir()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		lock, err := tryLockDir(dir)
		if err == nil {
			cfg.Frontend.setClient(nil, "")
			cfg.Display.ResetDevices()
			runErr := runOwner(ctx, cfg, dir, lock)
			if runErr != nil && ctx.Err() == nil {
				cfg.Log.Warn("relay stopped", "err", runErr)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(200 * time.Millisecond):
				}
			}
			continue
		}
		if err := runViewer(ctx, cfg, dir); err != nil && ctx.Err() == nil {
			cfg.Log.Info("status hub unavailable", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func runOwner(ctx context.Context, cfg Config, dir string, lock *os.File) error {
	defer lock.Close()
	_ = writeOwner(dir, cfg.Kind)
	defer os.Remove(filepath.Join(dir, ownerName))

	ln, err := listenHub(dir)
	if err != nil {
		return err
	}
	hctx, hcancel := context.WithCancel(ctx)
	defer hcancel()
	hubDone := make(chan error, 1)
	go func() {
		hubDone <- serveHub(hctx, ln, dir, cfg.Kind, cfg.Display, cfg.Control)
		hcancel()
	}()
	runErr := Run(hctx, cfg)
	hcancel()
	hubErr := <-hubDone
	if runErr != nil {
		return runErr
	}
	return hubErr
}

func runViewer(ctx context.Context, cfg Config, dir string) error {
	client, err := DialHub(dir)
	if err != nil {
		return err
	}
	defer client.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClose()

	if cfg.PCAP != nil {
		cfg.Log.Warn("pcap ignored while another revtether owns the relay")
	}

	fe := cfg.Frontend
	info := readOwner(dir)
	kind := info.Kind
	if kind == "" {
		kind = kindCLI
	}
	fe.setClient(client, kind)
	defer fe.setClient(nil, "")
	defer cfg.Display.SetSnapshot(nil)

	if info.PID != 0 {
		cfg.Display.Logf("同步 %s (pid %d) 的连接状态", ownerLabel(kind), info.PID)
	} else {
		cfg.Display.Logf("同步已运行的 revtether 的连接状态")
	}

	first := true
	var lastLogs []string
	for {
		snap, err := client.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if snap.Kind != "" {
			fe.setClient(client, snap.Kind)
		}
		if !first {
			for _, line := range diffLogs(lastLogs, snap.Logs) {
				cfg.Display.PrintLog(line)
			}
		}
		first = false
		lastLogs = snap.Logs
		devs := snap.Devices
		if cfg.Device != "" {
			devs = filterDevice(devs, cfg.Device)
		}
		cfg.Display.SetSnapshot(devs)
	}
}

func filterDevice(views []ui.DeviceView, want string) []ui.DeviceView {
	var out []ui.DeviceView
	for _, v := range views {
		if v.ID == want || v.Serial == want {
			out = append(out, v)
		}
	}
	return out
}

func diffLogs(prev, next []string) []string {
	if len(prev) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(prev))
	for _, p := range prev {
		seen[p] = true
	}
	var out []string
	for _, n := range next {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}
