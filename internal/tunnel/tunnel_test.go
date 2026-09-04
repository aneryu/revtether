package tunnel

import (
	"bufio"
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
