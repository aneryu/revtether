package adb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"revtether/internal/transport"
)

const (
	adbAddr    = "127.0.0.1:5037"
	devicePort = 31416
	statusLen  = 4
)

var (
	ErrUnavailable = errors.New("adb: server not available")
	ErrEOF         = errors.New("adb: remote closed (is the device app running?)")
)

type Client struct {
	mu       sync.Mutex
	forwards map[string]int // serial -> local port
}

func New() *Client {
	return &Client{forwards: map[string]int{}}
}

func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", adbAddr)
	if err != nil {
		if startErr := startServer(ctx); startErr != nil {
			return nil, fmt.Errorf("%w: %v (adb start-server: %v)", ErrUnavailable, err, startErr)
		}
		conn, err = d.DialContext(ctx, "tcp", adbAddr)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return &controlConn{Conn: conn, stop: stop}, nil
}

// ADB protocol reads must stop when the caller cancels, not only its TCP dial.
type controlConn struct {
	net.Conn
	stop func() bool
}

func (c *controlConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

func startServer(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "adb", "start-server")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, bytesPreview(out))
	}
	return nil
}

func bytesPreview(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func sendRequest(w io.Writer, cmd string) error {
	if len(cmd) > 0xffff {
		return errors.New("adb: request too long")
	}
	_, err := fmt.Fprintf(w, "%04x%s", len(cmd), cmd)
	return err
}

func readStatus(r io.Reader) error {
	var st [statusLen]byte
	if _, err := io.ReadFull(r, st[:]); err != nil {
		return err
	}
	switch string(st[:]) {
	case "OKAY":
		return nil
	case "FAIL":
		msg, err := readHexBlob(r)
		if err != nil {
			return fmt.Errorf("adb FAIL (unreadable): %w", err)
		}
		return fmt.Errorf("adb: %s", msg)
	default:
		return fmt.Errorf("adb: unexpected status %q", st)
	}
}

func readHexBlob(r io.Reader) (string, error) {
	var hex [4]byte
	if _, err := io.ReadFull(r, hex[:]); err != nil {
		return "", err
	}
	n, err := parseHex4(hex[:])
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func parseHex4(b []byte) (int, error) {
	if len(b) != 4 {
		return 0, errors.New("adb: bad hex length")
	}
	var n int
	for _, c := range b {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n += int(c - 'A' + 10)
		default:
			return 0, fmt.Errorf("adb: invalid hex %q", b)
		}
	}
	return n, nil
}

func parseDeviceLines(text string, notifyUnauthorized func(serial string)) []transport.DeviceEvent {
	var out []transport.DeviceEvent
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		serial, state := fields[0], fields[1]
		extras := strings.Join(fields[2:], " ")
		switch state {
		case "device":
			out = append(out, transport.DeviceEvent{
				ID:       serial,
				Serial:   serial,
				Name:     androidModel(extras),
				Platform: transport.Android,
				Attached: true,
			})
		case "unauthorized":
			if notifyUnauthorized != nil {
				notifyUnauthorized(serial)
			}
		}
	}
	return out
}

func androidModel(extras string) string {
	for _, f := range strings.Fields(extras) {
		k, v, ok := strings.Cut(f, ":")
		if ok && k == "model" && v != "" {
			return strings.ReplaceAll(v, "_", " ")
		}
	}
	return ""
}

func (c *Client) lookupName(ctx context.Context, serial string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if list, err := c.ListDevices(ctx); err == nil {
		for _, d := range list {
			if d.ID == serial && d.Name != "" {
				return d.Name
			}
		}
	}
	return c.lookupNameFromProp(ctx, serial)
}

func (c *Client) lookupNameFromProp(ctx context.Context, serial string) string {
	conn, err := c.connect(ctx)
	if err != nil {
		return ""
	}
	defer conn.Close()
	cmd := fmt.Sprintf("host-serial:%s:shell:getprop ro.product.model", serial)
	if err := sendRequest(conn, cmd); err != nil {
		return ""
	}
	if err := readStatus(conn); err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(conn, 256))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r", ""))
}

func (c *Client) Watch(ctx context.Context) (<-chan transport.DeviceEvent, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	if err := sendRequest(conn, "host:track-devices"); err != nil {
		conn.Close()
		return nil, err
	}
	if err := readStatus(conn); err != nil {
		conn.Close()
		return nil, err
	}

	ch := make(chan transport.DeviceEvent, 16)
	go func() {
		defer close(ch)
		defer conn.Close()
		known := map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			blob, err := readHexBlob(conn)
			if err != nil {
				return
			}
			var unauthorized []string
			live := parseDeviceLines(blob, func(s string) { unauthorized = append(unauthorized, s) })
			seen := map[string]bool{}
			for _, ev := range live {
				seen[ev.ID] = true
				if !known[ev.ID] {
					known[ev.ID] = true
					if ev.Name == "" {
						ev.Name = c.lookupName(ctx, ev.ID)
					}
					if ev.Name == "" {
						ev.Name = "Android"
					}
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			for _, s := range unauthorized {
				select {
				case ch <- transport.DeviceEvent{
					ID:       s,
					Serial:   s,
					Platform: transport.Android,
					Attached: false,
					Message:  "请在设备上允许 USB 调试",
				}:
				case <-ctx.Done():
					return
				}
			}
			for id := range known {
				if !seen[id] {
					delete(known, id)
					select {
					case ch <- transport.DeviceEvent{
						ID:       id,
						Serial:   id,
						Platform: transport.Android,
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

func pickPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func (c *Client) Connect(ctx context.Context, deviceID string, port uint16) (io.ReadWriteCloser, error) {
	if port == 0 {
		port = devicePort
	}
	local, err := pickPort()
	if err != nil {
		return nil, err
	}
	if err := c.forward(ctx, deviceID, local, int(port)); err != nil {
		return nil, err
	}

	var d net.Dialer
	stream, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", local))
	if err != nil {
		_ = c.killForward(context.Background(), deviceID, local)
		return nil, err
	}
	c.mu.Lock()
	c.forwards[deviceID] = local
	c.mu.Unlock()
	return &forwardConn{Conn: stream, client: c, serial: deviceID, local: local}, nil
}

func (c *Client) forward(ctx context.Context, serial string, local, remote int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	cmd := fmt.Sprintf("host-serial:%s:forward:tcp:%d;tcp:%d", serial, local, remote)
	if err := sendRequest(conn, cmd); err != nil {
		return err
	}
	if err := readStatus(conn); err != nil {
		return err
	}
	// 第二次 OKAY：forward 安装成功（adb handle_forward_request）
	if err := readStatus(conn); err != nil {
		return err
	}
	return nil
}

func (c *Client) killForward(ctx context.Context, serial string, local int) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	cmd := fmt.Sprintf("host-serial:%s:killforward:tcp:%d", serial, local)
	if err := sendRequest(conn, cmd); err != nil {
		return err
	}
	if err := readStatus(conn); err != nil {
		return err
	}
	return nil
}

func (c *Client) ListDevices(ctx context.Context) ([]transport.DeviceEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := sendRequest(conn, "host:devices-l"); err != nil {
		return nil, err
	}
	if err := readStatus(conn); err != nil {
		return nil, err
	}
	blob, err := readHexBlob(conn)
	if err != nil {
		return nil, err
	}
	return parseDeviceLines(blob, nil), nil
}

type forwardConn struct {
	net.Conn
	client *Client
	serial string
	local  int
	once   sync.Once
}

func (f *forwardConn) Close() error {
	var err error
	f.once.Do(func() {
		err = f.Conn.Close()
		_ = f.client.killForward(context.Background(), f.serial, f.local)
		f.client.mu.Lock()
		if f.client.forwards[f.serial] == f.local {
			delete(f.client.forwards, f.serial)
		}
		f.client.mu.Unlock()
	})
	return err
}

// ParseHex4 exported for tests.
func ParseHex4(b []byte) (int, error) { return parseHex4(b) }

// ParseDeviceLines exported for tests.
func ParseDeviceLines(text string) []transport.DeviceEvent {
	return parseDeviceLines(text, nil)
}
