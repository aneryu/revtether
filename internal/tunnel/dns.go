package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"revtether/internal/resolvconf"
)

var ErrNoNameserver = errors.New("dns: no nameserver")

const dnsTypeAAAA = 28

func truncated(resp []byte) bool {
	return len(resp) >= 3 && resp[2]&0x02 != 0
}

func skipDNSName(msg []byte, i int) int {
	for i < len(msg) {
		l := int(msg[i])
		if l == 0 {
			return i + 1
		}
		if l&0xc0 == 0xc0 {
			if i+2 > len(msg) {
				return -1
			}
			return i + 2
		}
		if l&0xc0 != 0 {
			return -1
		}
		i += 1 + l
	}
	return -1
}

func dnsQuestionType(q []byte) uint16 {
	if len(q) < 12 {
		return 0
	}
	i := skipDNSName(q, 12)
	if i < 0 || i+2 > len(q) {
		return 0
	}
	return binary.BigEndian.Uint16(q[i : i+2])
}

// emptyDNSReply is a NOERROR/NODATA response for the first question.
// Used for AAAA on an IPv4-only tunnel so clients do not wait on unreachable IPv6.
func emptyDNSReply(q []byte) []byte {
	if len(q) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(q[4:6]))
	if qd < 1 {
		qd = 1
	}
	end := 12
	for n := 0; n < qd; n++ {
		end = skipDNSName(q, end)
		if end < 0 || end+4 > len(q) {
			end = len(q)
			break
		}
		end += 4
	}
	out := append([]byte(nil), q[:end]...)
	rd := out[2] & 0x01
	out[2] = 0x80 | rd
	out[3] = 0x80
	binary.BigEndian.PutUint16(out[6:], 0)
	binary.BigEndian.PutUint16(out[8:], 0)
	binary.BigEndian.PutUint16(out[10:], 0)
	return out
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
	if dnsQuestionType(q) == dnsTypeAAAA {
		if resp := emptyDNSReply(q); resp != nil {
			return resp, nil
		}
	}
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
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > 65535 {
		return nil, errors.New("dns: bad tcp length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
