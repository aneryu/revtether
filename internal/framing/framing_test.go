package framing

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := bytes.Repeat([]byte{0xab}, 20)
	if err := Write(&buf, TypeIP, payload); err != nil {
		t.Fatal(err)
	}
	gotT, gotP, err := Read(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if gotT != TypeIP {
		t.Fatalf("type %d", gotT)
	}
	if !bytes.Equal(gotP, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestEmptyKeepalive(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, TypeKeepalive, nil); err != nil {
		t.Fatal(err)
	}
	gotT, gotP, err := Read(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if gotT != TypeKeepalive || len(gotP) != 0 {
		t.Fatalf("got type=%d len=%d", gotT, len(gotP))
	}
}

func TestMaxPayload(t *testing.T) {
	var buf bytes.Buffer
	p := make([]byte, MaxPayload)
	if err := Write(&buf, TypeIP, p); err != nil {
		t.Fatal(err)
	}
	_, got, err := Read(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxPayload {
		t.Fatalf("len %d", len(got))
	}
}

func TestWriteTooLarge(t *testing.T) {
	err := Write(io.Discard, TypeIP, make([]byte, MaxPayload+1))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestReadTooLarge(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{TypeIP, 0x05, 0xdd}) // 1501
	buf.Write(make([]byte, 8))
	_, _, err := Read(bufio.NewReader(&buf))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestReadTruncatedHeader(t *testing.T) {
	_, _, err := Read(bufio.NewReader(bytes.NewReader([]byte{TypeIP, 0x00})))
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}

func TestReadTruncatedPayload(t *testing.T) {
	_, _, err := Read(bufio.NewReader(bytes.NewReader([]byte{TypeIP, 0x00, 0x04, 0x01})))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v", err)
	}
}

func TestHelloCodec(t *testing.T) {
	p := EncodeHello(PlatformIOS)
	ver, plat, caps, err := DecodeHello(p)
	if err != nil || ver != Version || plat != PlatformIOS || caps != 0 {
		t.Fatalf("ver=%d plat=%d caps=%d err=%v", ver, plat, caps, err)
	}
	if _, _, _, err := DecodeHello([]byte{1, 1}); !errors.Is(err, ErrHello) {
		t.Fatalf("short hello: %v", err)
	}
}

func TestReadPayloadIndependent(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, TypeIP, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&buf, TypeIP, []byte{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(&buf)
	_, a, err := Read(r)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := Read(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, []byte{1, 2, 3}) || !bytes.Equal(b, []byte{4, 5, 6}) {
		t.Fatalf("a=%v b=%v", a, b)
	}
}
