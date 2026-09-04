package transport

import (
	"context"
	"io"
)

type Platform uint8

const (
	Android Platform = 0
	IOS     Platform = 1
)

func (p Platform) String() string {
	switch p {
	case Android:
		return "Android"
	case IOS:
		return "iOS"
	default:
		return "unknown"
	}
}

type DeviceEvent struct {
	ID       string // usbmux DeviceID 或 adb serial
	Serial   string // UDID / serial，用于显示
	Platform Platform
	Attached bool
	Message  string // 可选提示，如 unauthorized
}

type Transport interface {
	Watch(ctx context.Context) (<-chan DeviceEvent, error)
	Connect(ctx context.Context, deviceID string, port uint16) (io.ReadWriteCloser, error)
}
