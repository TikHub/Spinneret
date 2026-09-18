package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tcpPair returns two connected TCP connections.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	dialed, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	server, ok := <-accepted
	require.True(t, ok)
	t.Cleanup(func() {
		_ = dialed.Close()
		_ = server.Close()
	})
	return dialed, server
}

func TestConnTracker(t *testing.T) {
	t.Parallel()
	tr := newConnTracker()
	a, b := tcpPair(t)
	require.True(t, tr.add(a, b))
	require.Equal(t, 2, tr.len())
	tr.remove(a)
	require.Equal(t, 1, tr.len())

	tr.closeAll()
	// b was closed by closeAll: its peer observes EOF.
	_, err := b.Write([]byte("x"))
	require.Error(t, err)
	require.False(t, tr.add(a), "no tracking after closeAll")
	require.Equal(t, 1, tr.len())
}

func TestPipeHalfClose(t *testing.T) {
	t.Parallel()
	clientSide, proxyClient := tcpPair(t)
	proxyUpstream, upstreamSide := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pipe(proxyClient, proxyUpstream, time.Second)
	}()

	_, err := clientSide.Write([]byte("ping"))
	require.NoError(t, err)
	require.NoError(t, clientSide.(*net.TCPConn).CloseWrite())

	// Upstream reads the payload followed by EOF thanks to the half-close.
	got, err := io.ReadAll(upstreamSide)
	require.NoError(t, err)
	require.Equal(t, "ping", string(got))

	// The reverse direction still works after the half-close.
	_, err = upstreamSide.Write([]byte("pong"))
	require.NoError(t, err)
	require.NoError(t, upstreamSide.Close())
	got, err = io.ReadAll(clientSide)
	require.NoError(t, err)
	require.Equal(t, "pong", string(got))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestPipeIdleTimeout(t *testing.T) {
	t.Parallel()
	_, proxyClient := tcpPair(t)
	proxyUpstream, _ := tcpPair(t)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		pipe(proxyClient, proxyUpstream, 50*time.Millisecond)
	}()
	select {
	case <-done:
		require.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("idle tunnel was not torn down")
	}
}

func TestPipeIdleTimeoutTracksBothDirections(t *testing.T) {
	t.Parallel()
	const idle = 150 * time.Millisecond
	clientSide, proxyClient := tcpPair(t)
	proxyUpstream, upstreamSide := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pipe(proxyClient, proxyUpstream, idle)
	}()

	// The client sends nothing while the upstream streams a response for several idle periods:
	// the tunnel must stay open because it is not idle as a whole.
	const chunks = 12
	go func() {
		for range chunks {
			if _, err := upstreamSide.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(idle / 3)
		}
	}()
	require.NoError(t, clientSide.SetReadDeadline(time.Now().Add(10*time.Second)))
	buf := make([]byte, chunks)
	_, err := io.ReadFull(clientSide, buf)
	require.NoError(t, err, "tunnel was torn down while the upstream direction was active")

	// Once both directions are quiet, the tunnel is closed after roughly one idle period.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle tunnel was not torn down")
	}
	_, err = clientSide.Read(make([]byte, 1))
	require.Error(t, err)
}

func TestIdleReader(t *testing.T) {
	t.Parallel()
	t.Run("deadline error", func(t *testing.T) {
		t.Parallel()
		a, _ := tcpPair(t)
		require.NoError(t, a.Close())
		_, err := idleReader{conn: a, timeout: time.Second, activity: newTunnelActivity()}.Read(make([]byte, 1))
		require.Error(t, err)
	})
	t.Run("no timeout reads directly", func(t *testing.T) {
		t.Parallel()
		a, b := tcpPair(t)
		_, err := b.Write([]byte("hi"))
		require.NoError(t, err)
		buf := make([]byte, 2)
		n, err := io.ReadFull(idleReader{conn: a}, buf)
		require.NoError(t, err)
		require.Equal(t, "hi", string(buf[:n]))
	})
	t.Run("times out when the tunnel is idle", func(t *testing.T) {
		t.Parallel()
		a, _ := tcpPair(t)
		activity := newTunnelActivity()
		start := time.Now()
		_, err := idleReader{conn: a, timeout: 50 * time.Millisecond, activity: activity}.Read(make([]byte, 1))
		var ne net.Error
		require.ErrorAs(t, err, &ne)
		require.True(t, ne.Timeout())
		require.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
	})
}
