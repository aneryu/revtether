package tunnel

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"revtether/internal/dialer"
)

const (
	maxUDPSessions = 1024
	udpIdle        = 60 * time.Second
	udpIdleCheck   = 10 * time.Second
)

type Sessions struct {
	n   atomic.Int64
	max int64
}

func newSessions(max int64) *Sessions { return &Sessions{max: max} }

func (s *Sessions) TryAcquire() bool {
	for {
		n := s.n.Load()
		if n >= s.max {
			return false
		}
		if s.n.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (s *Sessions) Release() {
	s.n.Add(-1)
}

func (s *Sessions) Count() int64 { return s.n.Load() }

func installUDP(ctx context.Context, s *stack.Stack, d dialer.Dialer, log *slog.Logger, st *Stats, sessions *Sessions) {
	ufwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		if !sessions.TryAcquire() {
			return true
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			sessions.Release()
			return true
		}
		devConn := gonet.NewUDPConn(&wq, ep)
		id := r.ID()
		if st != nil {
			st.UDP.Add(1)
		}
		if id.LocalPort == 53 {
			go func() {
				handleDNS(ctx, devConn, sessions)
				if st != nil {
					st.UDP.Add(-1)
				}
			}()
			return true
		}
		target := dialTarget(net.IP(id.LocalAddress.AsSlice()), id.LocalPort)
		hostConn, err := d.DialContext(ctx, "udp", target)
		if err != nil {
			_ = devConn.Close()
			sessions.Release()
			if st != nil {
				st.UDP.Add(-1)
			}
			return true
		}
		if log != nil {
			log.Debug("udp open", "target", target)
		}
		go func() {
			stopClose := context.AfterFunc(ctx, func() {
				_ = devConn.Close()
				_ = hostConn.Close()
			})
			defer stopClose()
			relayUDP(devConn, hostConn, udpIdle, sessions)
			if st != nil {
				st.UDP.Add(-1)
			}
			if log != nil {
				log.Debug("udp close", "target", target)
			}
		}()
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, ufwd.HandlePacket)
}

func relayUDP(dev, host net.Conn, idle time.Duration, sess *Sessions) {
	defer sess.Release()
	defer dev.Close()
	defer host.Close()

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	touch := func() { last.Store(time.Now().UnixNano()) }

	var wg sync.WaitGroup
	wg.Add(2)
	copyDir := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				touch()
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go copyDir(host, dev)
	go copyDir(dev, host)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	t := time.NewTicker(udpIdleCheck)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if time.Since(time.Unix(0, last.Load())) > idle {
				_ = dev.Close()
				_ = host.Close()
				return
			}
		}
	}
}
