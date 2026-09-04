package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"revtether/internal/dialer"
	"revtether/internal/framing"
)

const (
	channelSize  = 256
	mtu          = 1400
	outQueue     = 512
	keepaliveInt = 5 * time.Second
	watchdogIdle = 15 * time.Second
)

var (
	ErrProtocol = framing.ErrProtocol
	ErrVersion  = framing.ErrVersion
)

type Config struct {
	Dialer dialer.Dialer
	Log    *slog.Logger
	PCAP   io.Writer
	Stats  *Stats
}

func Run(ctx context.Context, stream io.ReadWriteCloser, d dialer.Dialer, log *slog.Logger) error {
	return RunConfig(ctx, stream, Config{Dialer: d, Log: log})
}

func RunConfig(ctx context.Context, stream io.ReadWriteCloser, cfg Config) error {
	if cfg.Dialer == nil {
		cfg.Dialer = dialer.Direct
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Stats == nil {
		cfg.Stats = &Stats{}
	}

	var pcap *pcapWriter
	if cfg.PCAP != nil {
		cfg.Log.Info("pcap init", "writer", fmt.Sprintf("%T", cfg.PCAP))
		var err error
		pcap, err = newPCAP(cfg.PCAP)
		if err != nil {
			cfg.Log.Info("pcap init failed", "error", err.Error(), "err_type", fmt.Sprintf("%T", err))
			return fmt.Errorf("pcap: %w", err)
		}
	}

	cfg.Log.Info("handshake begin", "stream", fmt.Sprintf("%T", stream))
	hsStart := time.Now()
	br := bufio.NewReaderSize(stream, 64*1024)
	t, p, err := framing.Read(br)
	if err != nil {
		cfg.Log.Info("handshake read failed",
			"elapsed", time.Since(hsStart).String(),
			"error", err.Error(),
			"err_type", fmt.Sprintf("%T", err),
		)
		return fmt.Errorf("handshake read: %w", err)
	}
	cfg.Log.Info("handshake read ok",
		"elapsed", time.Since(hsStart).String(),
		"type", t,
		"payload_hex", fmt.Sprintf("%x", p),
		"buffered", br.Buffered(),
	)
	if t != framing.TypeHello {
		return fmt.Errorf("%w: first type=0x%02x payload=%x", ErrProtocol, t, p)
	}
	ver, plat, _, err := framing.DecodeHello(p)
	if err != nil {
		return fmt.Errorf("handshake hello: %w", err)
	}
	if ver != framing.Version {
		return ErrVersion
	}
	if err := framing.Write(stream, framing.TypeHello, framing.EncodeHello(framing.PlatformHost)); err != nil {
		cfg.Log.Info("handshake write failed",
			"elapsed", time.Since(hsStart).String(),
			"error", err.Error(),
			"err_type", fmt.Sprintf("%T", err),
		)
		return fmt.Errorf("handshake write: %w", err)
	}
	cfg.Log.Info("handshake ok", "device_platform", plat, "elapsed", time.Since(hsStart).String())

	ep := channel.New(channelSize, mtu, "")
	s, err := newStack(ep)
	if err != nil {
		ep.Close()
		return err
	}
	sessions := newSessions(maxUDPSessions)
	installTCP(ctx, s, cfg.Dialer, cfg.Log, cfg.Stats)
	installUDP(ctx, s, cfg.Dialer, cfg.Log, cfg.Stats, sessions)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lw := newFrameWriter(bufio.NewWriterSize(stream, 64*1024))

	out := make(chan []byte, outQueue)
	enqueue := func(pkt []byte) {
		select {
		case out <- pkt:
		default:
		}
	}

	var lastSeen atomic.Int64
	lastSeen.Store(time.Now().UnixNano())

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer cancel()
		return inbound(ctx, br, ep, enqueue, pcap, cfg.Stats, &lastSeen)
	})
	g.Go(func() error {
		defer cancel()
		return outbound(ctx, ep, enqueue)
	})
	g.Go(func() error {
		defer cancel()
		return writer(ctx, lw, out, pcap, cfg.Stats)
	})
	g.Go(func() error {
		defer cancel()
		return watchdog(ctx, &lastSeen)
	})

	err = g.Wait()
	cfg.Log.Info("tunnel loops done",
		"error", errStr(err),
		"err_type", fmt.Sprintf("%T", err),
		"unwrap", errStr(errors.Unwrap(err)),
	)
	_ = stream.Close()
	ep.Close()
	s.Close()
	s.Destroy()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("loop: %w", err)
	}
	return nil
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func inbound(ctx context.Context, br *bufio.Reader, ep *channel.Endpoint, enqueue func([]byte), pcap *pcapWriter, st *Stats, lastSeen *atomic.Int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, p, err := framing.Read(br)
		if err != nil {
			cfgLog := slog.Default()
			cfgLog.Info("inbound read failed", "error", err.Error(), "err_type", fmt.Sprintf("%T", err), "buffered", br.Buffered())
			return fmt.Errorf("inbound: %w", err)
		}
		lastSeen.Store(time.Now().UnixNano())
		switch t {
		case framing.TypeKeepalive:
		case framing.TypeHello:
		case framing.TypeIP:
			if len(p) < 1 || p[0]>>4 != 4 {
				continue
			}
			if reply, ok := icmpEcho(p); ok {
				enqueue(reply)
				continue
			}
			if st != nil {
				st.BytesUp.Add(uint64(len(p)))
			}
			pcap.writeIP(p)
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
			ep.InjectInbound(ipv4.ProtocolNumber, pkt)
			pkt.DecRef()
		default:
			return ErrProtocol
		}
	}
}

func outbound(ctx context.Context, ep *channel.Endpoint, enqueue func([]byte)) error {
	for {
		pkt := ep.ReadContext(ctx)
		if pkt == nil {
			return ctx.Err()
		}
		v := pkt.ToView()
		enqueue(append([]byte(nil), v.AsSlice()...))
		v.Release()
		pkt.DecRef()
	}
}

func writer(ctx context.Context, lw *frameWriter, out <-chan []byte, pcap *pcapWriter, st *Stats) error {
	tick := time.NewTicker(keepaliveInt)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p := <-out:
			if err := lw.write(framing.TypeIP, p); err != nil {
				slog.Info("writer ip failed", "error", err.Error(), "err_type", fmt.Sprintf("%T", err), "pkt", len(p))
				return fmt.Errorf("writer: %w", err)
			}
			if st != nil {
				st.BytesDown.Add(uint64(len(p)))
			}
			pcap.writeIP(p)
			for {
				select {
				case p2 := <-out:
					if err := lw.write(framing.TypeIP, p2); err != nil {
						return fmt.Errorf("writer: %w", err)
					}
					if st != nil {
						st.BytesDown.Add(uint64(len(p2)))
					}
					pcap.writeIP(p2)
				default:
					goto next
				}
			}
		case <-tick.C:
			if err := lw.write(framing.TypeKeepalive, nil); err != nil {
				slog.Info("writer keepalive failed", "error", err.Error(), "err_type", fmt.Sprintf("%T", err))
				return fmt.Errorf("writer: %w", err)
			}
		}
	next:
	}
}

func watchdog(ctx context.Context, lastSeen *atomic.Int64) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if time.Since(time.Unix(0, lastSeen.Load())) > watchdogIdle {
				return errors.New("tunnel: idle timeout")
			}
		}
	}
}
