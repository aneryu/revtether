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
	"syscall"
	"time"

	"revtether/internal/relay"
	"revtether/internal/transport/adb"
	"revtether/internal/transport/usbmux"
	"revtether/internal/tunnel"
)

var version = "dev"

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

	fe := relay.NewFrontend(os.Stderr)
	disp := fe.Display
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

	return relay.RunShared(ctx, relay.Config{
		Device:   *device,
		Log:      log,
		Display:  disp,
		Frontend: fe,
		PCAP:     pcapWriter,
		Kind:     "cli",
	})
}

func cmdDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	mux := usbmux.New()
	if list, err := mux.ListDevices(); err != nil {
		fmt.Fprintf(os.Stderr, "usbmux: %v\n", err)
	} else if len(list) == 0 {
		fmt.Println("iOS: (none)")
	} else {
		for _, d := range list {
			name := d.Name
			if name == "" {
				name = "iPhone"
			}
			fmt.Printf("iOS  %s  %s\n", name, d.Serial)
		}
	}

	adbCtx, adbCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer adbCancel()
	adbc := adb.New()
	if list, err := adbc.ListDevices(adbCtx); err != nil {
		fmt.Fprintf(os.Stderr, "adb: %v\n", err)
	} else if len(list) == 0 {
		fmt.Println("Android: (none)")
	} else {
		for _, d := range list {
			name := d.Name
			if name == "" {
				name = d.Serial
			}
			fmt.Printf("Android  %s  %s\n", name, d.Serial)
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
	stream, err := mux.Connect(ctx, d.ID, relay.ListenPort)
	if err != nil {
		fmt.Printf("connect: %v\n", err)
		return nil
	}
	defer stream.Close()
	fmt.Printf("connect: ok  type=%T\n", stream)

	for _, sz := range []int{7, 64, 4096, 65536} {
		stream.Close()
		stream, err = mux.Connect(ctx, d.ID, relay.ListenPort)
		if err != nil {
			fmt.Printf("connect(%d): %v\n", sz, err)
			return nil
		}
		buf := make([]byte, sz)
		n, err := stream.Read(buf)
		fmt.Printf("read size=%d n=%d err=%q hex=%x\n", sz, n, errString(err), buf[:max(0, min(n, 16))])
	}
	stream.Close()
	stream, err = mux.Connect(ctx, d.ID, relay.ListenPort)
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
