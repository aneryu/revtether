//go:build darwin

package main

import (
	"testing"

	"revtether/internal/transport"
	"revtether/internal/ui"
)

func TestDeviceTitle(t *testing.T) {
	named := ui.DeviceView{Name: "陈宇哲的iPhone", Platform: transport.IOS}
	if got := deviceTitle(named); got != "陈宇哲的iPhone" {
		t.Fatalf("title = %q", got)
	}
	fallback := ui.DeviceView{Serial: "00008030-000D35203C83802E", Platform: transport.IOS}
	if got := deviceTitle(fallback); got != "iPhone" {
		t.Fatalf("title = %q", got)
	}
	android := ui.DeviceView{Name: "Pixel 4a", Platform: transport.Android}
	if got := deviceTitle(android); got != "Pixel 4a" {
		t.Fatalf("title = %q", got)
	}

	waiting := ui.DeviceView{Name: "Pixel 4a", State: ui.StateWaiting}
	if got := deviceTitle(waiting); got != "Pixel 4a · 等待 App" {
		t.Fatalf("waiting title = %q", got)
	}
	paused := ui.DeviceView{Name: "Pixel 4a", State: ui.StateOff}
	if got := deviceTitle(paused); got != "Pixel 4a · 已暂停" {
		t.Fatalf("paused title = %q", got)
	}
}
