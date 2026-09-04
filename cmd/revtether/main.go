package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"revtether/internal/dialer"
	"revtether/internal/framing"
	"revtether/internal/transport"
	"revtether/internal/transport/adb"
	"revtether/internal/transport/usbmux"
	"revtether/internal/tunnel"
	"revtether/internal/ui"
)

var version = "dev"

const listenPort = 31416

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: revtether <run|devices|install|version>")
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "devices":
		return cmdDevices(args[1:])
	case "probe":
		return cmdProbe(args[1:])
	case "install":
		return cmdInstall(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("revtether %s\n", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	device := fs.String("device", "", "only this device id / serial")
	verbose := fs.Bool("verbose", false, "log each connection")
	pcapPath := fs.String("pcap", "", "write raw IP frames to pcap file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	log.Info("revtether run starting", "verbose", *verbose)

	var pcapWriter io.Writer
	if *pcapPath != "" {
		f, err := os.Create(*pcapPath)
		if err != nil {
			return err
		}
		defer f.Close()
		pcapWriter = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	disp := ui.New(os.Stderr)
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
	}
	loops := map[string]running{}
	var loopsMu sync.Mutex

	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				disp.Render()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			loopsMu.Lock()
			for _, r := range loops {
				r.cancel()
			}
			loopsMu.Unlock()
			return nil
		case ev, ok := <-merged:
			if !ok {
				<-ctx.Done()
				return nil
			}
			if *device != "" && ev.ID != *device && ev.Serial != *device {
				continue
			}
			if ev.Message != "" {
				disp.Logf("%s %s: %s", ev.Platform, ev.Serial, ev.Message)
			}
			if ev.Attached {
				loopsMu.Lock()
				if _, exists := loops[ev.ID]; exists {
					loopsMu.Unlock()
					continue
				}
				dctx, cancel := context.WithCancel(ctx)
				loops[ev.ID] = running{cancel: cancel}
				loopsMu.Unlock()

				var tr transport.Transport
				if ev.Platform == transport.IOS {
					tr = mux
				} else {
					tr = adbc
				}
				disp.Upsert(ui.Device{
					ID: ev.ID, Serial: ev.Serial, Platform: ev.Platform,
					State: ui.StateWaiting,
				})
				go deviceLoop(dctx, ev, tr, log, disp, pcapWriter, func() {
					loopsMu.Lock()
					delete(loops, ev.ID)
					loopsMu.Unlock()
					disp.Remove(ev.ID)
				})
			} else {
				loopsMu.Lock()
				if r, ok := loops[ev.ID]; ok {
					r.cancel()
					delete(loops, ev.ID)
				}
				loopsMu.Unlock()
				disp.Remove(ev.ID)
			}
		}
	}
}

func deviceLoop(ctx context.Context, ev transport.DeviceEvent, tr transport.Transport, log *slog.Logger, disp *ui.Display, pcap io.Writer, done func()) {
	defer done()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		disp.SetState(ev.ID, ui.StateWaiting, "等待 App 监听 31416")
		log.Info("deviceLoop connect",
			"id", ev.ID, "serial", ev.Serial, "platform", ev.Platform.String(),
			"transport", fmt.Sprintf("%T", tr), "port", listenPort,
		)
		stream, err := tr.Connect(ctx, ev.ID, listenPort)
		if err != nil {
			if ctx.Err() != nil {
				return
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
		err = tunnel.RunConfig(ctx, stream, tunnel.Config{
			Dialer: dialer.Direct,
			Log:    log.With("device", ev.Serial, "platform", ev.Platform.String()),
			PCAP:   pcap,
			Stats:  st,
		})
		log.Info("deviceLoop RunConfig returned",
			"device", ev.Serial,
			"elapsed", time.Since(runStart).String(),
			"error", errString(err),
			"err_type", fmt.Sprintf("%T", err),
			"unwrap", errString(errors.Unwrap(err)),
		)
		_ = stream.Close()
		if ctx.Err() != nil {
			return
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

func cmdDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mux := usbmux.New()
	if list, err := mux.ListDevices(); err != nil {
		fmt.Fprintf(os.Stderr, "usbmux: %v\n", err)
	} else if len(list) == 0 {
		fmt.Println("iOS: (none)")
	} else {
		for _, d := range list {
			fmt.Printf("iOS  %s  %s\n", d.ID, d.Serial)
		}
	}

	adbc := adb.New()
	if list, err := adbc.ListDevices(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "adb: %v\n", err)
	} else if len(list) == 0 {
		fmt.Println("Android: (none)")
	} else {
		for _, d := range list {
			fmt.Printf("Android  %s\n", d.Serial)
		}
	}
	return nil
}

func cmdProbe(args []string) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	mux := usbmux.New()
	list, err := mux.ListDevices()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("no iOS devices")
	}
	d := list[0]
	fmt.Printf("device id=%s serial=%s\n", d.ID, d.Serial)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := mux.Connect(ctx, d.ID, listenPort)
	if err != nil {
		fmt.Printf("connect: %v\n", err)
		return nil
	}
	defer stream.Close()
	fmt.Printf("connect: ok  type=%T\n", stream)

	for _, sz := range []int{7, 64, 4096, 65536} {
		stream.Close()
		stream, err = mux.Connect(ctx, d.ID, listenPort)
		if err != nil {
			fmt.Printf("connect(%d): %v\n", sz, err)
			return nil
		}
		buf := make([]byte, sz)
		n, err := stream.Read(buf)
		fmt.Printf("read size=%d n=%d err=%q hex=%x\n", sz, n, errString(err), buf[:max(0, min(n, 16))])
	}
	stream.Close()
	stream, err = mux.Connect(ctx, d.ID, listenPort)
	if err != nil {
		fmt.Printf("connect(run): %v\n", err)
		return nil
	}
	time.Sleep(80 * time.Millisecond)
	fmt.Println("slept 80ms after connect")
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = tunnel.RunConfig(rctx, stream, tunnel.Config{Log: slog.New(slog.NewTextHandler(os.Stdout, nil))})
	fmt.Printf("RunConfig err=%q type=%T unwrap=%q\n", errString(err), err, errString(errors.Unwrap(err)))
	return nil
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	device := fs.String("device", "", "adb serial")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = device
	return errors.New("revtether install: APK not embedded yet (M2)")
}
