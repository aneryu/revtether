package tunnel

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"revtether/internal/dialer"
)

func installTCP(ctx context.Context, s *stack.Stack, d dialer.Dialer, log *slog.Logger, st *Stats) {
	fwd := tcp.NewForwarder(s, 256<<10, 4096, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		target := dialTarget(net.IP(id.LocalAddress.AsSlice()), id.LocalPort)
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		hostConn, err := d.DialContext(dctx, "tcp", target)
		cancel()
		if err != nil {
			r.Complete(true)
			if log != nil {
				log.Debug("tcp dial failed", "target", target, "err", err)
			}
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			_ = hostConn.Close()
			r.Complete(true)
			return
		}
		r.Complete(false)
		devConn := gonet.NewTCPConn(&wq, ep)
		if st != nil {
			st.TCP.Add(1)
		}
		if log != nil {
			log.Debug("tcp open", "target", target)
		}
		go func() {
			relayTCP(devConn, hostConn)
			if st != nil {
				st.TCP.Add(-1)
			}
			if log != nil {
				log.Debug("tcp close", "target", target)
			}
		}()
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}

func relayTCP(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		_, _ = io.CopyBuffer(dst, src, buf)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}
