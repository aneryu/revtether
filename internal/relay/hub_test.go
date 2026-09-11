//go:build unix

package relay

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"revtether/internal/ui"
)

func TestHubSnapshotAndEnable(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	disp := ui.New(io.Discard)
	ctrl := NewControl()
	disp.Upsert(ui.Device{ID: "dev1", Serial: "pixel", Name: "Pixel 4a", State: ui.StateConnected})

	errCh := make(chan error, 1)
	go func() {
		errCh <- ServeHub(ctx, dir, kindCLI, disp, ctrl)
	}()

	waitSock(t, dir)

	c, err := DialHub(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	snap, err := c.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Kind != kindCLI || len(snap.Devices) != 1 || snap.Devices[0].ID != "dev1" {
		t.Fatalf("snapshot = %#v", snap)
	}
	if !snap.Devices[0].Enabled {
		t.Fatal("device should start enabled")
	}

	if err := c.SetEnabled("dev1", false); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, err = c.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Devices) == 1 && !snap.Devices[0].Enabled && snap.Devices[0].State == ui.StateOff {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw disable: %#v", snap.Devices)
		}
	}
	if ctrl.Enabled("dev1") {
		t.Fatal("owner control should be off")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not exit")
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		if _, err := c.Recv(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("hub left its viewer connection open after stopping")
			}
			break
		}
	}
}

func TestFilterDeviceAndDiffLogs(t *testing.T) {
	views := []ui.DeviceView{
		{ID: "a", Serial: "s1"},
		{ID: "b", Serial: "s2"},
	}
	got := filterDevice(views, "s2")
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("filter = %#v", got)
	}
	diff := diffLogs([]string{"1", "2"}, []string{"2", "3"})
	if len(diff) != 1 || diff[0] != "3" {
		t.Fatalf("diff = %#v", diff)
	}
	if diffLogs(nil, []string{"1"}) != nil {
		t.Fatal("first sync should skip historical logs")
	}
}

func waitSock(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, sockName)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		c, err := DialHub(dir)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hub socket not ready")
}
