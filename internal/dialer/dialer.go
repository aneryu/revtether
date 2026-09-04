package dialer

import (
	"context"
	"net"
	"time"
)

type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

var Direct Dialer = &net.Dialer{Timeout: 10 * time.Second}
