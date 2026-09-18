package main

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// connTracker tracks hijacked connections, which http.Server.Shutdown does not manage.
type connTracker struct {
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// newConnTracker creates an empty tracker.
func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[net.Conn]struct{})}
}

// add tracks conns; it returns false (tracking nothing) once the tracker has been closed.
func (t *connTracker) add(conns ...net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	for _, c := range conns {
		t.conns[c] = struct{}{}
	}
	return true
}

// remove stops tracking conns.
func (t *connTracker) remove(conns ...net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range conns {
		delete(t.conns, c)
	}
}

// len returns the number of tracked connections.
func (t *connTracker) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}

// closeAll closes every tracked connection and rejects new ones.
func (t *connTracker) closeAll() {
	t.mu.Lock()
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.closed = true
	t.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// tunnelActivity records when bytes last crossed a tunnel in either direction.
type tunnelActivity struct {
	last atomic.Int64 // unix nanoseconds
}

// newTunnelActivity creates an activity tracker stamped with the current time.
func newTunnelActivity() *tunnelActivity {
	a := &tunnelActivity{}
	a.touch()
	return a
}

// touch marks the tunnel as active now.
func (a *tunnelActivity) touch() {
	a.last.Store(time.Now().UnixNano())
}

// idleDeadline returns the instant the tunnel becomes idle for timeout.
func (a *tunnelActivity) idleDeadline(timeout time.Duration) time.Time {
	return time.Unix(0, a.last.Load()).Add(timeout)
}

// idleReader reads from a tunnel connection and fails only once the whole tunnel, not just this
// direction, has been idle for timeout. A direction waiting for a long response therefore
// survives as long as bytes keep flowing the other way.
type idleReader struct {
	conn     net.Conn
	timeout  time.Duration // 0 = no limit
	activity *tunnelActivity
}

// Read implements io.Reader.
func (r idleReader) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.conn.Read(p)
	}
	deadline := time.Now().Add(r.timeout)
	for {
		if err := r.conn.SetReadDeadline(deadline); err != nil {
			return 0, err
		}
		n, err := r.conn.Read(p)
		if n > 0 {
			r.activity.touch()
			return n, err
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return n, err
		}
		// This direction timed out; keep waiting while the other direction was active recently.
		deadline = r.activity.idleDeadline(r.timeout)
		if !time.Now().Before(deadline) {
			return n, err
		}
	}
}

// closeWriter is implemented by connections supporting TCP half-close.
type closeWriter interface {
	CloseWrite() error
}

// pipe copies bytes in both directions until both sides are done, then closes both connections.
// A tunnel idle in both directions for longer than idleTimeout (0 = no limit) is torn down.
func pipe(a, b net.Conn, idleTimeout time.Duration) {
	activity := newTunnelActivity()
	var wg sync.WaitGroup
	wg.Go(func() { copyHalf(b, idleReader{conn: a, timeout: idleTimeout, activity: activity}) })
	wg.Go(func() { copyHalf(a, idleReader{conn: b, timeout: idleTimeout, activity: activity}) })
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

// copyHalf copies src to dst; a clean EOF half-closes dst, any error tears down both sides.
func copyHalf(dst net.Conn, src idleReader) {
	_, err := io.Copy(dst, src)
	if cw, ok := dst.(closeWriter); ok && err == nil {
		if cw.CloseWrite() == nil {
			return
		}
	}
	_ = dst.Close()
	_ = src.conn.Close()
}
