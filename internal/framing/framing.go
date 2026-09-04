package framing

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

const (
	TypeHello     byte = 0x00
	TypeIP        byte = 0x01
	TypeKeepalive byte = 0x02

	MaxPayload      = 1500
	Version    byte = 1

	PlatformAndroid byte = 0
	PlatformIOS     byte = 1
	PlatformHost    byte = 0xFF
)

var (
	ErrFrameTooLarge = errors.New("framing: payload too large")
	ErrHello         = errors.New("framing: invalid hello")
	ErrProtocol      = errors.New("framing: protocol error")
	ErrVersion       = errors.New("framing: version mismatch")
)

func Write(w io.Writer, t byte, p []byte) error {
	if len(p) > MaxPayload {
		return ErrFrameTooLarge
	}
	var h [3]byte
	h[0] = t
	binary.BigEndian.PutUint16(h[1:], uint16(len(p)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

// Read 返回的 payload 是新分配的切片，调用方可持有。
func Read(r *bufio.Reader) (t byte, p []byte, err error) {
	var h [3]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(h[1:]))
	if n > MaxPayload {
		return 0, nil, ErrFrameTooLarge
	}
	p = make([]byte, n)
	_, err = io.ReadFull(r, p)
	return h[0], p, err
}

func EncodeHello(platform byte) []byte {
	return []byte{Version, platform, 0, 0}
}

func DecodeHello(p []byte) (version, platform byte, caps uint16, err error) {
	if len(p) < 4 {
		return 0, 0, 0, ErrHello
	}
	return p[0], p[1], binary.BigEndian.Uint16(p[2:4]), nil
}
