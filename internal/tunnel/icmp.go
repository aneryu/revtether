package tunnel

import "gvisor.dev/gvisor/pkg/tcpip/header"

func icmpEcho(p []byte) ([]byte, bool) {
	if len(p) < header.IPv4MinimumSize+header.ICMPv4MinimumSize {
		return nil, false
	}
	if p[0]>>4 != 4 {
		return nil, false
	}
	iph := header.IPv4(p)
	if !iph.IsValid(len(p)) {
		return nil, false
	}
	if iph.Protocol() != uint8(header.ICMPv4ProtocolNumber) {
		return nil, false
	}
	hlen := int(iph.HeaderLength())
	if len(p) < hlen+header.ICMPv4MinimumSize {
		return nil, false
	}
	icmp := header.ICMPv4(p[hlen:])
	if icmp.Type() != header.ICMPv4Echo {
		return nil, false
	}

	out := append([]byte(nil), p...)
	rip := header.IPv4(out)
	src := rip.SourceAddress()
	rip.SetSourceAddress(rip.DestinationAddress())
	rip.SetDestinationAddress(src)
	rip.SetTTL(64)

	ricmp := header.ICMPv4(out[hlen:])
	ricmp.SetType(header.ICMPv4EchoReply)
	ricmp.SetChecksum(0)
	ricmp.SetChecksum(header.ICMPv4Checksum(ricmp, 0))

	rip.SetChecksum(0)
	rip.SetChecksum(^rip.CalculateChecksum())
	return out, true
}
