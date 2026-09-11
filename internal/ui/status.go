package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
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
	StateOff          State = "paused"
	StateError        State = "error"
	maxLogs                 = 12
)

type Device struct {
	ID       string
	Serial   string
	Name     string
	Platform transport.Platform
	State    State
	Detail   string
	Stats    *tunnel.Stats
}

type DeviceView struct {
	ID       string             `json:"id"`
	Serial   string             `json:"serial"`
	Name     string             `json:"name"`
	Platform transport.Platform `json:"platform"`
	State    State              `json:"state"`
	Detail   string             `json:"detail"`
	TCP      int64              `json:"tcp"`
	UDP      int64              `json:"udp"`
	UpBps    float64            `json:"up_bps"`
	DownBps  float64            `json:"down_bps"`
	Enabled  bool               `json:"enabled"`
}

func (v DeviceView) Line() string {
	label := v.Name
	if label == "" {
		label = v.Serial
		if label == "" {
			label = v.ID
		}
		label = trimSerial(label)
	}
	line := fmt.Sprintf("[%s %s]  %s  tcp:%d udp:%d  ↑%s ↓%s",
		v.Platform, label,
		v.State, v.TCP, v.UDP,
		formatRate(v.UpBps), formatRate(v.DownBps),
	)
	if v.Detail != "" && v.State != StateConnected {
		line += "  " + v.Detail
	}
	return line
}

type Display struct {
	mu      sync.Mutex
	out     io.Writer
	devices map[string]*Device
	prev    map[string]snap
	logs    []string
	lines   int
	fixed   []DeviceView
	last    []DeviceView
	hasLast bool
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
		delete(d.prev, id)
	}
}

func (d *Display) Snapshot() []DeviceView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked(time.Now())
}

// LastSnapshot returns the most recently computed views without advancing
// rate counters. The status hub uses this so CLI Render stays the sole
// consumer of per-interval byte deltas.
func (d *Display) LastSnapshot() []DeviceView {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fixed != nil {
		return copyViews(d.fixed)
	}
	if d.hasLast {
		return copyViews(d.last)
	}
	return d.snapshotLocked(time.Now())
}

func (d *Display) SetSnapshot(views []DeviceView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if views == nil {
		d.fixed = nil
		return
	}
	d.fixed = copyViews(views)
}

func (d *Display) ResetDevices() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.devices = map[string]*Device{}
	d.prev = map[string]snap{}
	d.fixed = nil
	d.last = nil
	d.hasLast = false
}

func (d *Display) PatchEnabled(id string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.fixed {
		if d.fixed[i].ID == id {
			d.fixed[i].Enabled = on
			if !on {
				d.fixed[i].State = StateOff
				d.fixed[i].Detail = ""
			}
		}
	}
}

func (d *Display) Logs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.logs))
	copy(out, d.logs)
	return out
}

func (d *Display) Render() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lines > 0 {
		fmt.Fprintf(d.out, "\x1b[%dA\x1b[J", d.lines)
	}
	views := d.snapshotLocked(time.Now())
	if len(views) == 0 {
		fmt.Fprintln(d.out, "waiting for USB devices…")
		d.lines = 1
		return
	}
	for _, v := range views {
		fmt.Fprintln(d.out, v.Line())
	}
	d.lines = len(views)
}

func copyViews(in []DeviceView) []DeviceView {
	out := make([]DeviceView, len(in))
	copy(out, in)
	return out
}

func (d *Display) snapshotLocked(now time.Time) []DeviceView {
	if d.fixed != nil {
		return copyViews(d.fixed)
	}
	views := make([]DeviceView, 0, len(d.devices))
	for _, dev := range d.devices {
		tcp, udp, up, down := int64(0), int64(0), uint64(0), uint64(0)
		if dev.Stats != nil {
			tcp, udp, up, down = dev.Stats.Snapshot()
		}
		upBps, downBps := 0.0, 0.0
		if p, ok := d.prev[dev.ID]; ok {
			dt := now.Sub(p.at).Seconds()
			if dt > 0 && up >= p.up && down >= p.down {
				upBps = float64(up-p.up) / dt
				downBps = float64(down-p.down) / dt
			}
		}
		d.prev[dev.ID] = snap{up: up, down: down, at: now}
		views = append(views, DeviceView{
			ID:       dev.ID,
			Serial:   dev.Serial,
			Name:     dev.Name,
			Platform: dev.Platform,
			State:    dev.State,
			Detail:   dev.Detail,
			TCP:      tcp,
			UDP:      udp,
			UpBps:    upBps,
			DownBps:  downBps,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Platform != views[j].Platform {
			return views[i].Platform > views[j].Platform
		}
		si, sj := views[i].Serial, views[j].Serial
		if si == "" {
			si = views[i].ID
		}
		if sj == "" {
			sj = views[j].ID
		}
		return si < sj
	})
	d.last = copyViews(views)
	d.hasLast = true
	return views
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
	d.recordLog(fmt.Sprintf(format, args...), true)
}

func (d *Display) PrintLog(msg string) {
	d.recordLog(msg, false)
}

func (d *Display) recordLog(msg string, stamp bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lines > 0 {
		fmt.Fprintf(d.out, "\x1b[%dA\x1b[J", d.lines)
		d.lines = 0
	}
	fmt.Fprintln(d.out, msg)
	stored := msg
	if stamp {
		stored = time.Now().Format("15:04:05") + " " + msg
	}
	d.logs = append(d.logs, stored)
	if len(d.logs) > maxLogs {
		d.logs = d.logs[len(d.logs)-maxLogs:]
	}
}
