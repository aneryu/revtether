package tunnel

import (
	"encoding/binary"
	"io"
	"sync"
	"time"
)

const linkTypeRaw = 101

type pcapWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newPCAP(w io.Writer) (*pcapWriter, error) {
	var hdr [24]byte
	binary.LittleEndian.PutUint32(hdr[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:], 2)
	binary.LittleEndian.PutUint16(hdr[6:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 65535)
	binary.LittleEndian.PutUint32(hdr[20:], linkTypeRaw)
	if _, err := w.Write(hdr[:]); err != nil {
		return nil, err
	}
	return &pcapWriter{w: w}, nil
}

func (p *pcapWriter) writeIP(b []byte) {
	if p == nil || len(b) == 0 {
		return
	}
	now := time.Now()
	var rec [16]byte
	binary.LittleEndian.PutUint32(rec[0:], uint32(now.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(now.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(b)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(b)))
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.Write(rec[:])
	_, _ = p.w.Write(b)
}
