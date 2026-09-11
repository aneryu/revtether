package tunnel

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
)

func TestCaptureSerializesSharedSessions(t *testing.T) {
	var out bytes.Buffer
	capture, err := NewCapture(&out)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for session := range 4 {
		wg.Go(func() {
			for range 32 {
				capture.writeIP([]byte{0x45, byte(session), 0, 4})
			}
		})
	}
	wg.Wait()
	data := out.Bytes()
	if len(data) < 24 || binary.LittleEndian.Uint32(data) != 0xa1b2c3d4 {
		t.Fatal("missing PCAP header")
	}
	data = data[24:]
	counts := [4]int{}
	for len(data) > 0 {
		if len(data) < 20 || binary.LittleEndian.Uint32(data[8:]) != 4 ||
			binary.LittleEndian.Uint32(data[12:]) != 4 || data[16] != 0x45 || data[17] >= 4 {
			t.Fatal("interleaved record or repeated PCAP header")
		}
		counts[data[17]]++
		data = data[20:]
	}
	for _, n := range counts {
		if n != 32 {
			t.Fatalf("session counts: %v", counts)
		}
	}
}
