package tunnel

import "testing"

func TestUDPSessionsLimit(t *testing.T) {
	s := newSessions(3)
	if !s.TryAcquire() || !s.TryAcquire() || !s.TryAcquire() {
		t.Fatal("should acquire 3")
	}
	if s.TryAcquire() {
		t.Fatal("should reject 4th")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Fatal("should acquire after release")
	}
	if s.Count() != 3 {
		t.Fatalf("count %d", s.Count())
	}
}
