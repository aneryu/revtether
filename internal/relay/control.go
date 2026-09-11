package relay

import (
	"context"
	"sync"
)

// Control toggles forwarding per device. A nil Control leaves every device on.
type Control struct {
	mu      sync.Mutex
	off     map[string]bool
	waiters map[string][]chan struct{}
	runs    map[string]context.CancelFunc
}

func NewControl() *Control {
	return &Control{
		off:     map[string]bool{},
		waiters: map[string][]chan struct{}{},
		runs:    map[string]context.CancelFunc{},
	}
}

func (c *Control) Enabled(id string) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.off[id]
}

func (c *Control) SetEnabled(id string, on bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	var cancel context.CancelFunc
	if on {
		delete(c.off, id)
		for _, ch := range c.waiters[id] {
			close(ch)
		}
		c.waiters[id] = nil
	} else {
		c.off[id] = true
		cancel = c.runs[id]
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Control) WaitEnabled(ctx context.Context, id string) error {
	if c == nil {
		return ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		if !c.off[id] {
			c.mu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		c.waiters[id] = append(c.waiters[id], ch)
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (c *Control) setRun(id string, cancel context.CancelFunc) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.runs[id] = cancel
	c.mu.Unlock()
}

func (c *Control) clearRun(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.runs, id)
	c.mu.Unlock()
}

func (c *Control) Forget(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.off, id)
	delete(c.runs, id)
	for _, ch := range c.waiters[id] {
		close(ch)
	}
	delete(c.waiters, id)
	c.mu.Unlock()
}
