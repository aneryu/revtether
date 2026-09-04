package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"revtether/internal/resolvconf"
)

var ErrNoNameserver = errors.New("dns: no nameserver")

func truncated(resp []byte) bool {
	return len(resp) >= 3 && resp[2]&0x02 != 0
}

func handleDNS(ctx context.Context, c *gonet.UDPConn, sess *Sessions) {
	defer func() {
		_ = c.Close()
		sess.Release()
	}()
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		q := append([]byte(nil), buf[:n]...)
		go func() {
			resp, err := forwardQuery(ctx, q)
			if err != nil {
				return
			}
			_, _ = c.Write(resp)
		}()
	}
}

func forwardQuery(ctx context.Context, q []byte) ([]byte, error) {
	for _, ns := range resolvconf.Nameservers() {
		resp, err := exchangeUDP(ctx, ns, q, 3*time.Second)
		if err != nil {
			continue
		}
		if truncated(resp) {
			if r2, err := exchangeTCP(ctx, ns, q, 3*time.Second); err == nil {
				return r2, nil
			}
		}
		return resp, nil
	}
	return nil, ErrNoNameserver
}

func exchangeUDP(ctx context.Context, ns string, q []byte, timeout time.Duration) ([]byte, error) {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort(ns, "53"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buf[:n]...), nil
}

func exchangeTCP(ctx context.Context, ns string, q []byte, timeout time.Duration) ([]byte, error) {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ns, "53"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(q)))
	if _, err := c.Write(hdr[:]); err != nil {
		return nil, err
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	if _, err := c.Read(hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > 65535 {
		return nil, errors.New("dns: bad tcp length")
	}
	buf := make([]byte, n)
	if err := readFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readFull(c net.Conn, buf []byte) error {
	off := 0
	for off < len(buf) {
		n, err := c.Read(buf[off:])
		if err != nil {
			return err
		}
		off += n
	}
	return nil
}
