package tunnel

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const nicID tcpip.NICID = 1

func newStack(ep stack.LinkEndpoint) (*stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		HandleLocal:        false,
	})
	sack := tcpip.TCPSACKEnabled(true)
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("SACK: %v", err)
	}
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: 256 << 10, Max: 4 << 20}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("rcvbuf: %v", err)
	}
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: 256 << 10, Max: 4 << 20}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("sndbuf: %v", err)
	}
	mod := tcpip.TCPModerateReceiveBufferOption(true)
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &mod); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("modbuf: %v", err)
	}

	if err := s.CreateNIC(nicID, ep); err != nil {
		s.Destroy()
		return nil, fmt.Errorf("CreateNIC: %v", err)
	}
	s.SetPromiscuousMode(nicID, true)
	s.SetSpoofing(nicID, true)
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
	return s, nil
}
