package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"revtether/internal/framing"
)

func TestHandshakeVersionMismatch(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go func() {
		_ = framing.Write(a, framing.TypeHello, []byte{2, 1, 0, 0})
	}()

	err := Run(context.Background(), b, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != ErrVersion {
		t.Fatalf("got %v", err)
	}
}

func TestHandshakeThenKeepalive(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Run(ctx, b, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	if err := framing.Write(a, framing.TypeHello, framing.EncodeHello(framing.PlatformIOS)); err != nil {
		t.Fatal(err)
	}
	tpe, payload, err := framing.Read(bufio.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if tpe != framing.TypeHello {
		t.Fatalf("type %d", tpe)
	}
	ver, plat, _, err := framing.DecodeHello(payload)
	if err != nil || ver != framing.Version || plat != framing.PlatformHost {
		t.Fatalf("hello ver=%d plat=%d err=%v", ver, plat, err)
	}
	cancel()
	wg.Wait()
}

type readObservedConn struct {
	net.Conn
	reading chan struct{}
}

func (c *readObservedConn) Read(p []byte) (int, error) {
	c.reading <- struct{}{}
	return c.Conn.Read(p)
}

func TestCancelUnblocksStreamRead(t *testing.T) {
	for _, handshake := range []bool{false, true} {
		name := "waiting_for_hello"
		if handshake {
			name = "connected"
		}
		t.Run(name, func(t *testing.T) {
			peer, conn := net.Pipe()
			defer peer.Close()
			defer conn.Close()
			stream := &readObservedConn{Conn: conn, reading: make(chan struct{}, 8)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, stream, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			}()
			<-stream.reading
			if handshake {
				var hello bytes.Buffer
				_ = framing.Write(&hello, framing.TypeHello, framing.EncodeHello(framing.PlatformIOS))
				if _, err := peer.Write(hello.Bytes()); err != nil {
					t.Fatal(err)
				}
				if _, _, err := framing.Read(bufio.NewReader(peer)); err != nil {
					t.Fatal(err)
				}
				<-stream.reading
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = peer.Close()
				<-done
				t.Fatal("cancellation did not unblock the stream read")
			}
		})
	}
}
