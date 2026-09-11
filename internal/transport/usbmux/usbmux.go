package usbmux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"howett.net/plist"

	"revtether/internal/transport"
)

const (
	socketPath = "/var/run/usbmuxd"

	protoVersion uint32 = 1
	msgPlist     uint32 = 8

	ResultOK          = 0
	ResultBadCommand  = 1
	ResultBadDevice   = 2
	ResultConnRefused = 3

	ListenPort = 31416
)

var (
	ErrConnRefused = errors.New("usbmux: connection refused (is the device app running?)")
	ErrBadDevice   = errors.New("usbmux: device not found")
	ErrUnavailable = errors.New("usbmux: /var/run/usbmuxd not available")
)

type Client struct{}

func New() *Client { return &Client{} }

func htons(p uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], p)
	return binary.LittleEndian.Uint16(b[:])
}

func dial() (net.Conn, error) {
	c, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return c, nil
}

type muxHeader struct {
	Length  uint32
	Version uint32
	MsgType uint32
	Tag     uint32
}

func writePlist(c net.Conn, tag uint32, v any) error {
	body, err := plist.Marshal(v, plist.XMLFormat)
	if err != nil {
		return err
	}
	var h muxHeader
	h.Length = uint32(16 + len(body))
	h.Version = protoVersion
	h.MsgType = msgPlist
	h.Tag = tag
	if err := binary.Write(c, binary.LittleEndian, h); err != nil {
		return err
	}
	_, err = c.Write(body)
	return err
}

func readPacket(c net.Conn) (tag uint32, body []byte, err error) {
	var h muxHeader
	if err = binary.Read(c, binary.LittleEndian, &h); err != nil {
		return 0, nil, err
	}
	if h.Length < 16 {
		return 0, nil, fmt.Errorf("usbmux: short header length %d", h.Length)
	}
	n := int(h.Length - 16)
	body = make([]byte, n)
	if _, err = io.ReadFull(c, body); err != nil {
		return 0, nil, err
	}
	return h.Tag, body, nil
}

type plistMap map[string]any

func decodeMap(body []byte) (plistMap, error) {
	var v any
	if _, err := plist.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("usbmux: expected dict, got %T", v)
	}
	return m, nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case uint:
		return int(n), true
	case int32:
		return int(n), true
	case uint32:
		return int(n), true
	default:
		return 0, false
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func (m plistMap) str(k string) string { return asString(m[k]) }

func (m plistMap) int(k string) int {
	n, _ := asInt(m[k])
	return n
}

func (m plistMap) dict(k string) plistMap {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

func listenRequest() map[string]any {
	return map[string]any{
		"MessageType":         "Listen",
		"ClientVersionString": "revtether",
		"ProgName":            "revtether",
		"kLibUSBMuxVersion":   3,
	}
}

func connectRequest(deviceID int, port uint16) map[string]any {
	return map[string]any{
		"MessageType":         "Connect",
		"DeviceID":            deviceID,
		"PortNumber":          int(htons(port)),
		"ClientVersionString": "revtether",
		"ProgName":            "revtether",
	}
}

func listRequest() map[string]any {
	return map[string]any{
		"MessageType":         "ListDevices",
		"ClientVersionString": "revtether",
		"ProgName":            "revtether",
	}
}

func eventFromAttached(m plistMap) (transport.DeviceEvent, bool) {
	props := m.dict("Properties")
	if props == nil {
		return transport.DeviceEvent{}, false
	}
	if ct := props.str("ConnectionType"); ct != "" && ct != "USB" {
		return transport.DeviceEvent{}, false
	}
	id := m.int("DeviceID")
	serial := props.str("SerialNumber")
	productID := props.int("ProductID")
	return transport.DeviceEvent{
		ID:       strconv.Itoa(id),
		Serial:   serial,
		Name:     iosKind(productID),
		Platform: transport.IOS,
		Attached: true,
	}, true
}

func (c *Client) Watch(ctx context.Context) (<-chan transport.DeviceEvent, error) {
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	if err := writePlist(conn, 1, listenRequest()); err != nil {
		conn.Close()
		return nil, err
	}
	_, body, err := readPacket(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	m, err := decodeMap(body)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if m.str("MessageType") == "Result" && m.int("Number") != ResultOK {
		conn.Close()
		return nil, fmt.Errorf("usbmux: listen failed: %d", m.int("Number"))
	}
	slog.Info("usbmux watch listening")

	ch := make(chan transport.DeviceEvent, 16)
	go func() {
		defer close(ch)
		defer conn.Close()
		type pkt struct {
			body []byte
			err  error
		}
		incoming := make(chan pkt, 1)
		go func() {
			for {
				_, b, err := readPacket(conn)
				select {
				case incoming <- pkt{b, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-incoming:
				if p.err != nil {
					return
				}
				msg, err := decodeMap(p.body)
				if err != nil {
					continue
				}
				switch msg.str("MessageType") {
				case "Attached":
					if ev, ok := eventFromAttached(msg); ok {
						ev.Name = enrichIOSName(ev.ID, ev.Serial, msg.dict("Properties").int("ProductID"))
						slog.Info("usbmux watch attached", "id", ev.ID, "serial", ev.Serial, "name", ev.Name)
						select {
						case ch <- ev:
						case <-ctx.Done():
							return
						}
					}
				case "Detached":
					id := msg.int("DeviceID")
					select {
					case ch <- transport.DeviceEvent{
						ID:       strconv.Itoa(id),
						Platform: transport.IOS,
						Attached: false,
					}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch, nil
}

func (c *Client) Connect(ctx context.Context, deviceID string, port uint16) (io.ReadWriteCloser, error) {
	id, err := strconv.Atoi(deviceID)
	if err != nil {
		return nil, fmt.Errorf("usbmux: bad device id %q", deviceID)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	fd, fdErr := connFD(conn)
	slog.Info("usbmux connect dialed",
		"device_id", deviceID, "port", port, "fd", fd, "fd_err", errStr(fdErr),
		"local", conn.LocalAddr(), "remote", conn.RemoteAddr(),
		"fd_flags", fdFlags(fd),
	)
	tag := uint32(time.Now().UnixNano())
	if err := writePlist(conn, tag, connectRequest(id, port)); err != nil {
		slog.Info("usbmux connect writePlist failed", "error", err.Error(), "fd", fd)
		conn.Close()
		return nil, err
	}
	// Do not SetReadDeadline on this socket. After a successful Connect,
	// usbmuxd turns it into the device TCP stream; SO_RCVTIMEO on that
	// Darwin unix socket can make the first payload read return EINVAL.
	type pkt struct {
		body []byte
		err  error
	}
	ch := make(chan pkt, 1)
	go func() {
		_, body, err := readPacket(conn)
		if err != nil {
			slog.Info("usbmux connect result read failed", "error", err.Error(), "fd", fd)
		} else {
			slog.Info("usbmux connect result read ok", "body_len", len(body), "fd", fd, "fd_flags", fdFlags(fd), "so_error", soError(fd))
		}
		ch <- pkt{body, err}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var body []byte
	select {
	case <-ctx.Done():
		conn.Close()
		<-ch
		return nil, ctx.Err()
	case <-timer.C:
		conn.Close()
		<-ch
		return nil, fmt.Errorf("usbmux: connect timeout")
	case r := <-ch:
		if r.err != nil {
			conn.Close()
			return nil, r.err
		}
		body = r.body
	}
	m, err := decodeMap(body)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if m.str("MessageType") != "Result" {
		conn.Close()
		return nil, fmt.Errorf("usbmux: unexpected %q", m.str("MessageType"))
	}
	switch n := m.int("Number"); n {
	case ResultOK:
		slog.Info("usbmux connect result OK", "device_id", deviceID, "port", port, "fd", fd)
		return adaptStream(conn), nil
	case ResultConnRefused:
		conn.Close()
		return nil, ErrConnRefused
	case ResultBadDevice:
		conn.Close()
		return nil, ErrBadDevice
	default:
		n := m.int("Number")
		conn.Close()
		return nil, fmt.Errorf("usbmux: connect result %d", n)
	}
}

func (c *Client) ListDevices() ([]transport.DeviceEvent, error) {
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := writePlist(conn, 1, listRequest()); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, body, err := readPacket(conn)
	if err != nil {
		return nil, err
	}
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	raw, _ := m["DeviceList"].([]any)
	var out []transport.DeviceEvent
	for _, item := range raw {
		dm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if ev, ok := eventFromAttached(plistMap(dm)); ok {
			props := plistMap(dm).dict("Properties")
			var productID int
			if props != nil {
				productID = props.int("ProductID")
			}
			ev.Name = enrichIOSName(ev.ID, ev.Serial, productID)
			out = append(out, ev)
		}
	}
	return out, nil
}

// PortSwapped is htons(port); exported for tests.
func PortSwapped(port uint16) uint16 { return htons(port) }
