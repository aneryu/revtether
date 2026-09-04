package tunnel

import (
	"net"
	"strconv"
)

var hostGateway = net.IPv4(198, 18, 0, 1).To4()

func dialTarget(ip net.IP, port uint16) string {
	if ip4 := ip.To4(); ip4 != nil && ip4.Equal(hostGateway) {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
}
