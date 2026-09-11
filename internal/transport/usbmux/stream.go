package usbmux

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// adaptStream takes the unix socket after a successful usbmux Connect.
// Keep the blocking-syscall workaround for Darwin usbmux I/O, including
// successful short reads/writes accompanied by EINVAL. The original net.Conn
// retains ownership of its descriptor and must be closed through net.Conn.Close
// so Go's finalizer cannot later close a reused descriptor.
func adaptStream(c net.Conn) io.ReadWriteCloser {
	// Linux usbmuxd supplies a regular stream handled by Go's network poller.
	if runtime.GOOS != "darwin" {
		return c
	}
	orig, origErr := connFD(c)
	fd, err := dupConnFD(c)
	if err != nil {
		slog.Error("usbmux adaptStream: dup failed, falling back to net.Conn",
			"conn", fmt.Sprintf("%T", c), "orig_fd", orig, "orig_fd_err", errStr(origErr), "error", err.Error())
		return c
	}
	nbErr := syscall.SetNonblock(fd, false)
	if nbErr != nil {
		_ = syscall.Close(fd)
		return c
	}
	s := &rawStream{fd: fd, hold: c, holdFD: orig}
	slog.Info("usbmux adaptStream",
		"conn", fmt.Sprintf("%T", c),
		"orig_fd", orig,
		"dup_fd", fd,
		"orig_flags", fdFlags(orig),
		"dup_flags", fdFlags(fd),
		"setnonblock_err", errStr(nbErr),
		"orig_so_error", soError(orig),
		"dup_so_error", soError(fd),
	)
	return s
}

func connFD(c net.Conn) (int, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return -1, errors.New("usbmux: conn has no SyscallConn")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var fd int
	if err := raw.Control(func(f uintptr) { fd = int(f) }); err != nil {
		return -1, err
	}
	return fd, nil
}

func dupConnFD(c net.Conn) (int, error) {
	fd, err := connFD(c)
	if err != nil {
		return -1, err
	}
	dup, err := syscall.Dup(fd)
	if err == nil {
		syscall.CloseOnExec(dup)
	}
	return dup, err
}

type rawStream struct {
	fd       int
	hold     net.Conn
	holdFD   int
	ops      atomic.Uint64
	mu       sync.RWMutex
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

func (s *rawStream) Read(p []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(p) == 0 {
		return 0, nil
	}
	for i := 0; i < 80; i++ {
		if s.closed.Load() {
			return 0, net.ErrClosed
		}
		n, err := syscall.Read(s.fd, p)
		s.trace("read", len(p), n, err, i)
		if n > 0 {
			return n, nil
		}
		switch err {
		case nil:
			return 0, io.EOF
		case syscall.EINTR:
			continue
		case syscall.EINVAL:
			time.Sleep(5 * time.Millisecond)
			continue
		default:
			return 0, err
		}
	}
	slog.Error("usbmux rawStream.Read exhausted EINVAL retries", "fd", s.fd, "size", len(p), "hold_fd", s.holdFD)
	return 0, syscall.EINVAL
}

func (s *rawStream) Write(p []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	off := 0
	for off < len(p) {
		if s.closed.Load() {
			return off, net.ErrClosed
		}
		n, err := syscall.Write(s.fd, p[off:])
		s.trace("write", len(p)-off, n, err, off)
		if n > 0 {
			off += n
			continue
		}
		switch err {
		case syscall.EINTR:
			continue
		case syscall.EINVAL:
			time.Sleep(5 * time.Millisecond)
			continue
		case nil:
			return off, io.ErrShortWrite
		default:
			return off, err
		}
	}
	return off, nil
}

func (s *rawStream) Close() error {
	s.once.Do(func() {
		s.closed.Store(true)
		// Wake blocking syscalls before waiting for their descriptor references.
		// Close alone need not interrupt a read on another thread.
		_ = syscall.Shutdown(s.fd, syscall.SHUT_RDWR)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closeErr = syscall.Close(s.fd)
		if s.hold != nil {
			_ = s.hold.Close()
		}
	})
	return s.closeErr
}

func (s *rawStream) trace(op string, size, n int, err error, extra int) {
	seq := s.ops.Add(1)
	if err == nil && seq > 8 {
		return
	}
	if err == nil || err == syscall.EINTR || err == syscall.EINVAL || seq <= 8 {
		slog.Debug("usbmux io",
			"seq", seq,
			"op", op,
			"fd", s.fd,
			"hold_fd", s.holdFD,
			"size", size,
			"n", n,
			"error", errStr(err),
			"extra", extra,
			"fd_flags", fdFlags(s.fd),
			"so_error", soError(s.fd),
		)
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fdFlags(fd int) string {
	if fd < 0 {
		return "closed"
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return "fcntl:" + err.Error()
	}
	s := "0"
	if flags&syscall.O_NONBLOCK != 0 {
		s = "NONBLOCK"
	} else {
		s = "BLOCK"
	}
	return s
}

func soError(fd int) string {
	if fd < 0 {
		return "closed"
	}
	v, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ERROR)
	if err != nil {
		return "getsockopt:" + err.Error()
	}
	if v == 0 {
		return "0"
	}
	return syscall.Errno(v).Error()
}
