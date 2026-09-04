package adb

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseHex4(t *testing.T) {
	n, err := ParseHex4([]byte("001a"))
	if err != nil || n != 26 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, err = ParseHex4([]byte("00FF"))
	if err != nil || n != 255 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := ParseHex4([]byte("00zz")); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDeviceLines(t *testing.T) {
	text := "emulator-5554\tdevice\nABC123\tunauthorized\nXYZ\toffline\n"
	evs := ParseDeviceLines(text)
	if len(evs) != 1 || evs[0].ID != "emulator-5554" || !evs[0].Attached {
		t.Fatalf("%+v", evs)
	}
}

func TestReadStatusOKAYFAIL(t *testing.T) {
	if err := readStatus(strings.NewReader("OKAY")); err != nil {
		t.Fatal(err)
	}
	err := readStatus(strings.NewReader("FAIL0005boom!"))
	if err == nil || !strings.Contains(err.Error(), "boom!") {
		t.Fatalf("got %v", err)
	}
}

func TestHexBlobRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_ = sendRequest(a, "host:track-devices")
		_, _ = a.Write([]byte("OKAY"))
		payload := "serial\tdevice\n"
		_, _ = fmt.Fprintf(a, "%04x%s", len(payload), payload)
	}()
	_ = b.SetDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(b)
	cmd, err := readHexBlob(r)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "host:track-devices" {
		t.Fatalf("cmd %q", cmd)
	}
	if err := readStatus(r); err != nil {
		t.Fatal(err)
	}
	body, err := readHexBlob(r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "serial\tdevice\n" {
		t.Fatalf("body %q", body)
	}
}

func TestSendRequestFormat(t *testing.T) {
	var buf strings.Builder
	if err := sendRequest(&buf, "host:version"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "000chost:version" {
		t.Fatalf("got %q", buf.String())
	}
}
