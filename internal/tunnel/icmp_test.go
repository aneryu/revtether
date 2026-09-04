package tunnel

import (
	"encoding/binary"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

func TestICMPEchoReply(t *testing.T) {
	pkt := makeICMPEcho(tcpip.AddrFrom4([4]byte{198, 18, 0, 2}), tcpip.AddrFrom4([4]byte{1, 1, 1, 1}), 0x1234, []byte("ping"))
	reply, ok := icmpEcho(pkt)
	if !ok {
		t.Fatal("expected echo reply")
	}
	rip := header.IPv4(reply)
	if rip.SourceAddress().String() != "1.1.1.1" || rip.DestinationAddress().String() != "198.18.0.2" {
		t.Fatalf("src=%s dst=%s", rip.SourceAddress(), rip.DestinationAddress())
	}
	if rip.TTL() != 64 {
		t.Fatalf("ttl %d", rip.TTL())
	}
	ricmp := header.ICMPv4(reply[rip.HeaderLength():])
	if ricmp.Type() != header.ICMPv4EchoReply {
		t.Fatalf("type %d", ricmp.Type())
	}
	if _, ok := icmpEcho(reply); ok {
		t.Fatal("reply of reply")
	}
}

func TestICMPIgnoreNonEcho(t *testing.T) {
	pkt := makeICMPEcho(tcpip.AddrFrom4([4]byte{198, 18, 0, 2}), tcpip.AddrFrom4([4]byte{1, 1, 1, 1}), 1, nil)
	iph := header.IPv4(pkt)
	icmp := header.ICMPv4(pkt[iph.HeaderLength():])
	icmp.SetType(header.ICMPv4DstUnreachable)
	if _, ok := icmpEcho(pkt); ok {
		t.Fatal("should ignore")
	}
	if _, ok := icmpEcho([]byte{0x60}); ok {
		t.Fatal("v6")
	}
}

func makeICMPEcho(src, dst tcpip.Address, id uint16, payload []byte) []byte {
	icmpLen := header.ICMPv4MinimumSize + len(payload)
	total := header.IPv4MinimumSize + icmpLen
	p := make([]byte, total)
	ip := header.IPv4(p)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(header.ICMPv4ProtocolNumber),
		SrcAddr:     src,
		DstAddr:     dst,
	})
	ip.SetChecksum(0)
	ip.SetChecksum(^ip.CalculateChecksum())

	icmp := header.ICMPv4(p[header.IPv4MinimumSize:])
	icmp.SetType(header.ICMPv4Echo)
	icmp.SetCode(header.ICMPv4UnusedCode)
	binary.BigEndian.PutUint16(p[header.IPv4MinimumSize+4:], id)
	copy(p[header.IPv4MinimumSize+header.ICMPv4MinimumSize:], payload)
	icmp.SetChecksum(0)
	icmp.SetChecksum(header.ICMPv4Checksum(icmp, 0))
	return p
}
