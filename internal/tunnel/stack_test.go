package tunnel

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type captureDialer struct {
	mu      sync.Mutex
	network string
	addr    string
	ch      chan struct{}
}

func (c *captureDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c.mu.Lock()
	c.network = network
	c.addr = addr
	c.mu.Unlock()
	select {
	case c.ch <- struct{}{}:
	default:
	}
	a, b := net.Pipe()
	go b.Close()
	return a, nil
}

func TestStackTCPForwarderDialsTarget(t *testing.T) {
	ep := channel.New(64, 1400, "")
	s, err := newStack(ep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Destroy()

	d := &captureDialer{ch: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	installTCP(ctx, s, d, nil, nil)

	pkt := makeTCPSYN(
		tcpip.AddrFrom4([4]byte{198, 18, 0, 2}),
		tcpip.AddrFrom4([4]byte{1, 2, 3, 4}),
		40000, 80,
	)
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
	ep.InjectInbound(ipv4.ProtocolNumber, pb)
	pb.DecRef()

	select {
	case <-d.ch:
	case <-ctx.Done():
		t.Fatal("dialer was not called")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.network != "tcp" || d.addr != "1.2.3.4:80" {
		t.Fatalf("dialed %s %s", d.network, d.addr)
	}
}

func makeTCPSYN(src, dst tcpip.Address, srcPort, dstPort uint16) []byte {
	tcpLen := header.TCPMinimumSize
	total := header.IPv4MinimumSize + tcpLen
	p := make([]byte, total)
	ip := header.IPv4(p)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(header.TCPProtocolNumber),
		SrcAddr:     src,
		DstAddr:     dst,
	})
	ip.SetChecksum(0)
	ip.SetChecksum(^ip.CalculateChecksum())

	th := header.TCP(p[header.IPv4MinimumSize:])
	th.Encode(&header.TCPFields{
		SrcPort:       srcPort,
		DstPort:       dstPort,
		SeqNum:        1,
		AckNum:        0,
		DataOffset:    header.TCPMinimumSize,
		Flags:         header.TCPFlagSyn,
		WindowSize:    65535,
		Checksum:      0,
		UrgentPointer: 0,
	})
	xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber, src, dst, uint16(tcpLen))
	th.SetChecksum(^th.CalculateChecksum(xsum))
	return p
}
