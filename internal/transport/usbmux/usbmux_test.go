package usbmux

import (
	"bytes"
	"errors"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"howett.net/plist"
)

func TestRawStreamEcho(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := rawStream{fd: fds[0]}, rawStream{fd: fds[1]}
	defer a.Close()
	defer b.Close()
	if _, err := a.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := b.Read(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("n=%d err=%v data=%q", n, err, buf[:n])
	}
}

func TestRawStreamConcurrentCloseUnblocksRead(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fds[1])
	s := &rawStream{fd: fds[0]}
	defer s.Close()
	done := make(chan error, 1)
	go func() {
		_, err := s.Read(make([]byte, 1))
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for s.mu.TryLock() {
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("read did not start")
		}
		runtime.Gosched()
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read succeeded after close")
		}
	case <-time.After(time.Second):
		// Closing the peer also releases a broken implementation's blocked read.
		_ = syscall.Shutdown(fds[1], syscall.SHUT_RDWR)
		t.Fatal("close did not unblock read")
	}
	wg.Wait()
	if _, err := s.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestAdaptStreamClosesOriginalConn(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fds[1])
	f := os.NewFile(uintptr(fds[0]), "usbmux-test")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream := adaptStream(conn)
	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("original net.Conn still owns its descriptor: %v", err)
	}
}

func TestHTONS31416(t *testing.T) {
	if got := PortSwapped(31416); got != 47226 {
		t.Fatalf("htons(31416)=%d want 47226", got)
	}
}

func TestConnectPlistPort(t *testing.T) {
	req := connectRequest(5, 31416)
	if req["PortNumber"] != int(47226) {
		t.Fatalf("PortNumber=%v", req["PortNumber"])
	}
	if req["DeviceID"] != 5 {
		t.Fatalf("DeviceID=%v", req["DeviceID"])
	}
	body, err := plist.Marshal(req, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decodeMap(body)
	if err != nil {
		t.Fatal(err)
	}
	if m.str("MessageType") != "Connect" || m.int("PortNumber") != 47226 {
		t.Fatalf("%v", m)
	}
}

func TestAttachedEventUSBOnly(t *testing.T) {
	xml := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>MessageType</key><string>Attached</string>
  <key>DeviceID</key><integer>7</integer>
  <key>Properties</key>
  <dict>
    <key>ConnectionType</key><string>USB</string>
    <key>SerialNumber</key><string>00008030-001A</string>
  </dict>
</dict>
</plist>`)
	m, err := decodeMap(xml)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := eventFromAttached(m)
	if !ok || ev.ID != "7" || ev.Serial != "00008030-001A" || !ev.Attached || ev.Name != "iPhone" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}

	netXML := bytes.ReplaceAll(xml, []byte("USB"), []byte("Network"))
	nm, err := decodeMap(netXML)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventFromAttached(nm); ok {
		t.Fatal("network device should be ignored")
	}
}

func TestIOSKind(t *testing.T) {
	if iosKind(0x12a8) != "iPhone" {
		t.Fatal(iosKind(0x12a8))
	}
	if iosKind(0x12ab) != "iPad" {
		t.Fatal(iosKind(0x12ab))
	}
}

func TestListenPlistRoundTrip(t *testing.T) {
	body, err := plist.Marshal(listenRequest(), plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decodeMap(body)
	if err != nil {
		t.Fatal(err)
	}
	if m.str("MessageType") != "Listen" || m.str("ProgName") != "revtether" {
		t.Fatalf("%v", m)
	}
}
