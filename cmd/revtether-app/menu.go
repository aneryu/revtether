//go:build darwin

package main

import (
	"revtether/internal/transport"
	"revtether/internal/ui"
)

func deviceTitle(v ui.DeviceView) string {
	name := deviceName(v)
	switch v.State {
	case ui.StateConnected, "":
		return name
	case ui.StateReconnecting:
		return name + " · 重连中"
	case ui.StateOff:
		return name + " · 已暂停"
	default:
		return name + " · 等待 App"
	}
}

func deviceName(v ui.DeviceView) string {
	if n := v.Name; n != "" {
		return n
	}
	switch v.Platform {
	case transport.IOS:
		return "iPhone"
	case transport.Android:
		return "Android"
	default:
		return "设备"
	}
}
