package relay

import (
	"context"
	"testing"
	"time"
)

func TestControlDefaultOn(t *testing.T) {
	c := NewControl()
	if !c.Enabled("dev") {
		t.Fatal("new device should be on")
	}
	if !(*Control)(nil).Enabled("dev") {
		t.Fatal("nil control should be on")
	}
}

func TestControlWaitAndToggle(t *testing.T) {
	c := NewControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.SetEnabled("a", false)
	if c.Enabled("a") {
		t.Fatal("expected off")
	}

	unblocked := make(chan struct{})
	go func() {
		if err := c.WaitEnabled(ctx, "a"); err != nil {
			t.Errorf("WaitEnabled: %v", err)
		}
		close(unblocked)
	}()

	select {
	case <-unblocked:
		t.Fatal("WaitEnabled returned while disabled")
	case <-time.After(40 * time.Millisecond):
	}

	c.SetEnabled("a", true)
	select {
	case <-unblocked:
	case <-time.After(time.Second):
		t.Fatal("WaitEnabled did not return after enable")
	}
}

func TestControlCancelsRun(t *testing.T) {
	c := NewControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx, pause := context.WithCancel(ctx)
	c.setRun("a", pause)
	c.SetEnabled("a", false)
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("disable should cancel the current run")
	}
}
