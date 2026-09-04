package tunnel

import "sync/atomic"

type Stats struct {
	TCP       atomic.Int64
	UDP       atomic.Int64
	BytesUp   atomic.Uint64
	BytesDown atomic.Uint64
}

func (s *Stats) Snapshot() (tcp, udp int64, up, down uint64) {
	if s == nil {
		return 0, 0, 0, 0
	}
	return s.TCP.Load(), s.UDP.Load(), s.BytesUp.Load(), s.BytesDown.Load()
}
