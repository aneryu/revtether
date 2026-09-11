package ui

import (
	"io"
	"strings"
	"testing"
	"time"

	"revtether/internal/transport"
	"revtether/internal/tunnel"
)

func TestSnapshotAndLogs(t *testing.T) {
	d := New(io.Discard)
	st := &tunnel.Stats{}
	st.TCP.Store(2)
	st.BytesUp.Store(100)
	d.Upsert(Device{
		ID: "1", Serial: "udid-long-serial-abcdef", Platform: transport.IOS,
		State: StateConnected, Stats: st,
	})
	d.Upsert(Device{
		ID: "2", Serial: "pixel", Platform: transport.Android,
		State: StateWaiting, Detail: "等待 App 监听 31416",
	})

	views := d.Snapshot()
	if len(views) != 2 {
		t.Fatalf("got %d devices", len(views))
	}
	if views[0].Platform != transport.IOS {
		t.Fatalf("expected iOS first, got %s", views[0].Platform)
	}
	line := views[0].Line()
	if !strings.Contains(line, "connected") || !strings.Contains(line, "tcp:2") {
		t.Fatalf("unexpected line %q", line)
	}
	if strings.Contains(views[0].Line(), "udid-long-serial-abcdef") {
		t.Fatalf("serial should be trimmed: %q", views[0].Line())
	}
	if !strings.Contains(views[1].Line(), "等待 App") {
		t.Fatalf("waiting detail missing: %q", views[1].Line())
	}

	d.Logf("hello %s", "world")
	logs := d.Logs()
	if len(logs) != 1 || !strings.HasSuffix(logs[0], "hello world") {
		t.Fatalf("logs = %#v", logs)
	}

	time.Sleep(20 * time.Millisecond)
	st.BytesUp.Store(100 + 2048)
	v2 := d.Snapshot()
	if v2[0].UpBps <= 0 {
		t.Fatalf("expected positive up rate, got %v", v2[0].UpBps)
	}

	last := d.LastSnapshot()
	if last[0].UpBps != v2[0].UpBps {
		t.Fatalf("LastSnapshot should not advance rates: %v vs %v", last[0].UpBps, v2[0].UpBps)
	}
}

func TestSetSnapshot(t *testing.T) {
	d := New(io.Discard)
	d.Upsert(Device{ID: "local", State: StateWaiting})
	d.SetSnapshot([]DeviceView{{
		ID: "remote", Name: "Pixel 4a", State: StateConnected, Enabled: true,
	}})
	views := d.Snapshot()
	if len(views) != 1 || views[0].ID != "remote" || !views[0].Enabled {
		t.Fatalf("remote snapshot = %#v", views)
	}
	d.PatchEnabled("remote", false)
	if d.Snapshot()[0].State != StateOff {
		t.Fatalf("expected paused after disable")
	}
	d.SetSnapshot(nil)
	d.ResetDevices()
	if len(d.Snapshot()) != 0 {
		t.Fatalf("expected empty after reset")
	}
}

func TestReconnectResetsRates(t *testing.T) {
	d := New(io.Discard)
	old := &tunnel.Stats{}
	old.BytesUp.Store(4096)
	old.BytesDown.Store(8192)
	d.Upsert(Device{ID: "dev", Stats: old})
	d.Snapshot()
	d.SetStats("dev", &tunnel.Stats{})
	v := d.Snapshot()[0]
	if v.UpBps != 0 || v.DownBps != 0 {
		t.Fatalf("new connection inherited old byte counters: %+v", v)
	}
}
