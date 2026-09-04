package tunnel

import "testing"

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
