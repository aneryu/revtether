package tunnel

import (
	"bufio"
	"sync"

	"revtether/internal/framing"
)

type frameWriter struct {
	mu sync.Mutex
	bw *bufio.Writer
}

func newFrameWriter(w *bufio.Writer) *frameWriter {
	return &frameWriter{bw: w}
}

func (w *frameWriter) write(t byte, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := framing.Write(w.bw, t, p); err != nil {
		return err
	}
	return w.bw.Flush()
}
