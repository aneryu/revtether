package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"revtether/internal/dialer"
	"revtether/internal/framing"
	"revtether/internal/transport"
	"revtether/internal/transport/adb"
	"revtether/internal/transport/usbmux"
	"revtether/internal/tunnel"
	"revtether/internal/ui"
)

const ListenPort uint16 = 31416

type Config struct {
	Device   string
	Log      *slog.Logger
	Display  *ui.Display
	PCAP     io.Writer
	Control  *Control
	Frontend *Frontend
	Kind     string // "cli" or "app"
	Dir      string // optional; default SupportDir()
	capture  *tunnel.Capture
}

func Run(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := cfg.prepareCapture(); err != nil {
		return err
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	disp := cfg.Display
	if disp == nil {
		disp = ui.New(io.Discard)
	}

	mux := usbmux.New()
	adbc := adb.New()

	merged := make(chan transport.DeviceEvent, 32)
	var wg sync.WaitGroup
	startWatch := func(name string, t transport.Transport) {
		ch, err := t.Watch(ctx)
		if err != nil {
			log.Warn("transport unavailable", "name", name, "err", err)
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					select {
					case merged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	startWatch("usbmux", mux)
	startWatch("adb", adbc)

	go func() {
		wg.Wait()
		close(merged)
	}()

	type running struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	loops := map[string]running{}
	stopDevice := func(id string) {
		if r, ok := loops[id]; ok {
			r.cancel()
			<-r.done
			delete(loops, id)
		}
		disp.Remove(id)
		cfg.Control.Forget(id)
	}
	defer func() {
		for _, r := range loops {
			r.cancel()
		}
		for id := range loops {
			stopDevice(id)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-merged:
			if !ok {
				<-ctx.Done()
				return nil
			}
			if cfg.Device != "" && ev.ID != cfg.Device && ev.Serial != cfg.Device {
				// usbmux detach events contain only the ID, not the selected serial.
				if _, tracked := loops[ev.ID]; ev.Attached || !tracked {
					continue
				}
			}
			if ev.Message != "" {
				disp.Logf("%s %s: %s", ev.Platform, ev.Serial, ev.Message)
			}
			if ev.Attached {
				if _, exists := loops[ev.ID]; exists {
					continue
				}
				dctx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				loops[ev.ID] = running{cancel: cancel, done: done}

				var tr transport.Transport
				if ev.Platform == transport.IOS {
					tr = mux
				} else {
					tr = adbc
				}
				disp.Upsert(ui.Device{
					ID: ev.ID, Serial: ev.Serial, Name: ev.Name, Platform: ev.Platform,
					State: ui.StateWaiting,
				})
				go deviceLoop(dctx, ev, tr, log, disp, cfg.capture, cfg.Control, func() {
					close(done)
				})
			} else {
				// Finish cleanup before a rapid reattach can reuse the same ID.
				stopDevice(ev.ID)
			}
		}
	}
}

func (cfg *Config) prepareCapture() error {
	if cfg.capture == nil && cfg.PCAP != nil {
		var err error
		cfg.capture, err = tunnel.NewCapture(cfg.PCAP)
		return err
	}
	return nil
}

func deviceLoop(ctx context.Context, ev transport.DeviceEvent, tr transport.Transport, log *slog.Logger, disp *ui.Display, pcap *tunnel.Capture, ctrl *Control, done func()) {
	defer done()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if !ctrl.Enabled(ev.ID) {
			disp.SetState(ev.ID, ui.StateOff, "")
		}
		if err := ctrl.WaitEnabled(ctx, ev.ID); err != nil {
			return
		}
		runCtx, pause := context.WithCancel(ctx)
		ctrl.setRun(ev.ID, pause)
		if !ctrl.Enabled(ev.ID) {
			pause()
			ctrl.clearRun(ev.ID)
			continue
		}

		disp.SetState(ev.ID, ui.StateWaiting, "等待 App 监听 31416")
		log.Info("deviceLoop connect",
			"id", ev.ID, "serial", ev.Serial, "platform", ev.Platform.String(),
			"transport", fmt.Sprintf("%T", tr), "port", ListenPort,
		)
		stream, err := tr.Connect(runCtx, ev.ID, ListenPort)
		if err != nil {
			pause()
			ctrl.clearRun(ev.ID)
			if ctx.Err() != nil {
				return
			}
			if !ctrl.Enabled(ev.ID) {
				continue
			}
			log.Info("connect failed", "device", ev.Serial, "error", err.Error(), "err_type", fmt.Sprintf("%T", err))
			disp.SetState(ev.ID, ui.StateWaiting, "无法连接 31416: "+err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		st := &tunnel.Stats{}
		disp.SetStats(ev.ID, st)
		disp.SetState(ev.ID, ui.StateConnected, "")
		log.Info("deviceLoop connected", "device", ev.Serial, "stream", fmt.Sprintf("%T", stream))
		runStart := time.Now()
		err = tunnel.RunConfig(runCtx, stream, tunnel.Config{
			Dialer:  dialer.Direct,
			Log:     log.With("device", ev.Serial, "platform", ev.Platform.String()),
			Capture: pcap,
			Stats:   st,
		})
		log.Info("deviceLoop RunConfig returned",
			"device", ev.Serial,
			"elapsed", time.Since(runStart).String(),
			"error", errString(err),
			"err_type", fmt.Sprintf("%T", err),
			"unwrap", errString(errors.Unwrap(err)),
		)
		_ = stream.Close()
		pause()
		ctrl.clearRun(ev.ID)
		if ctx.Err() != nil {
			return
		}
		if !ctrl.Enabled(ev.ID) {
			continue
		}
		msg := "连接断开"
		if err != nil {
			log.Info("tunnel closed", "device", ev.Serial, "error", errString(err), "err_type", fmt.Sprintf("%T", err))
			if errors.Is(err, framing.ErrVersion) {
				disp.Logf("协议版本不匹配，请升级设备 App")
				msg = "请升级设备 App"
			} else {
				msg = "连接断开: " + err.Error()
			}
		}
		disp.SetState(ev.ID, ui.StateReconnecting, msg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
