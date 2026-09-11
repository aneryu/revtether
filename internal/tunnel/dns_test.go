package tunnel

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestTruncatedBit(t *testing.T) {
	if truncated(nil) || truncated([]byte{0, 0}) {
		t.Fatal("short")
	}
	if truncated([]byte{0, 0, 0x00}) {
		t.Fatal("no TC")
	}
	if !truncated([]byte{0, 0, 0x02}) {
		t.Fatal("expected TC")
	}
	if !truncated([]byte{0, 0, 0x82}) { // QR + TC
		t.Fatal("expected TC with QR")
	}
}

func aaaaQuery() []byte {
	name := []byte{
		4, 'p', 'l', 'a', 'y',
		6, 'g', 'o', 'o', 'g', 'l', 'e',
		3, 'c', 'o', 'm',
		0,
	}
	q := make([]byte, 12+len(name)+4)
	binary.BigEndian.PutUint16(q[0:], 0x1234)
	q[2] = 0x01 // RD
	binary.BigEndian.PutUint16(q[4:], 1)
	copy(q[12:], name)
	binary.BigEndian.PutUint16(q[12+len(name):], dnsTypeAAAA)
	binary.BigEndian.PutUint16(q[12+len(name)+2:], 1)
	return q
}

func TestDNSQuestionTypeAAAA(t *testing.T) {
	if got := dnsQuestionType(aaaaQuery()); got != dnsTypeAAAA {
		t.Fatalf("qtype=%d", got)
	}
	if dnsQuestionType(nil) != 0 || dnsQuestionType([]byte{0, 1, 2}) != 0 {
		t.Fatal("short query")
	}
}

func TestEmptyDNSReplyNODATA(t *testing.T) {
	q := aaaaQuery()
	resp := emptyDNSReply(q)
	if resp == nil {
		t.Fatal("nil reply")
	}
	if resp[2]&0x80 == 0 {
		t.Fatal("QR not set")
	}
	if resp[3]&0x0f != 0 {
		t.Fatal("RCODE")
	}
	if binary.BigEndian.Uint16(resp[4:6]) != 1 {
		t.Fatal("QDCOUNT")
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Fatal("ANCOUNT")
	}
	if binary.BigEndian.Uint16(resp[8:10]) != 0 || binary.BigEndian.Uint16(resp[10:12]) != 0 {
		t.Fatal("NS/AR")
	}
	if dnsQuestionType(resp) != dnsTypeAAAA {
		t.Fatal("question not preserved")
	}
}

func TestForwardQueryAAAANoNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := forwardQuery(ctx, aaaaQuery())
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Fatal("expected NODATA")
	}
}
