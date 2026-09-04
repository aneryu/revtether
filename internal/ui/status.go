package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"revtether/internal/transport"
	"revtether/internal/tunnel"
)

type State string

const (
	StateWaiting      State = "waiting for app"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateError        State = "error"
)

type Device struct {
	ID       string
	Serial   string
	Platform transport.Platform
	State    State
	Detail   string
	Stats    *tunnel.Stats
}

type Display struct {
	mu      sync.Mutex
	out     io.Writer
	devices map[string]*Device
	prev    map[string]snap
	lines   int
}

type snap struct {
	up, down uint64
	at       time.Time
}

func New(out io.Writer) *Display {
	if out == nil {
		out = os.Stderr
	}
	return &Display{
		out:     out,
		devices: map[string]*Device{},
		prev:    map[string]snap{},
	}
}

func (d *Display) Upsert(dev Device) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := dev
	d.devices[dev.ID] = &cp
}

func (d *Display) Remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.devices, id)
	delete(d.prev, id)
}

func (d *Display) SetState(id string, st State, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev, ok := d.devices[id]; ok {
		dev.State = st
		dev.Detail = detail
	}
}

func (d *Display) SetStats(id string, st *tunnel.Stats) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev, ok := d.devices[id]; ok {
		dev.Stats = st
	}
}

func (d *Display) Render() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lines > 0 {
		fmt.Fprintf(d.out, "\x1b[%dA\x1b[J", d.lines)
	}
	if len(d.devices) == 0 {
		fmt.Fprintln(d.out, "waiting for USB devices…")
		d.lines = 1
		return
	}
	n := 0
	now := time.Now()
	for _, dev := range d.devices {
		tcp, udp, up, down := int64(0), int64(0), uint64(0), uint64(0)
		if dev.Stats != nil {
			tcp, udp, up, down = dev.Stats.Snapshot()
		}
		upBps, downBps := 0.0, 0.0
		if p, ok := d.prev[dev.ID]; ok {
			dt := now.Sub(p.at).Seconds()
			if dt > 0 {
				upBps = float64(up-p.up) / dt
				downBps = float64(down-p.down) / dt
			}
		}
		d.prev[dev.ID] = snap{up: up, down: down, at: now}
		serial := dev.Serial
		if serial == "" {
			serial = dev.ID
		}
		line := fmt.Sprintf("[%s %s]  %s  tcp:%d udp:%d  ↑%s ↓%s",
			dev.Platform, trimSerial(serial),
			dev.State, tcp, udp,
			formatRate(upBps), formatRate(downBps),
		)
		if dev.Detail != "" && dev.State != StateConnected {
			line += "  " + dev.Detail
		}
		fmt.Fprintln(d.out, line)
		n++
	}
	d.lines = n
}

func trimSerial(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

func formatRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	switch {
	case bps >= 1024*1024:
		return fmt.Sprintf("%.1fMB/s", bps/(1024*1024))
	case bps >= 1024:
		return fmt.Sprintf("%.0fKB/s", bps/1024)
	default:
		return fmt.Sprintf("%.0fB/s", bps)
	}
}

func (d *Display) Logf(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lines > 0 {
		fmt.Fprintf(d.out, "\x1b[%dA\x1b[J", d.lines)
		d.lines = 0
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(d.out, msg)
}
