package spinneret

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifyErrorTable(t *testing.T) {
	wrap := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://example.com/a?sig=1", Err: err}
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ErrorKindNone},
		{"context deadline", wrap(context.DeadlineExceeded), ErrorKindTimeout},
		{"os deadline", wrap(os.ErrDeadlineExceeded), ErrorKindTimeout},
		{"dns", wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}}), ErrorKindDNS},
		{"dns timeout", wrap(&net.DNSError{Err: "i/o timeout", Name: "x", IsTimeout: true}), ErrorKindDNS},
		{"refused", wrap(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), ErrorKindConnRefused},
		{"unreachable", wrap(syscall.EHOSTUNREACH), ErrorKindConnRefused},
		{"reset", wrap(&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}), ErrorKindConnReset},
		{"broken pipe", wrap(syscall.EPIPE), ErrorKindConnReset},
		{"eof", wrap(io.EOF), ErrorKindConnReset},
		{"unexpected eof", wrap(io.ErrUnexpectedEOF), ErrorKindConnReset},
		{"unknown authority", wrap(x509.UnknownAuthorityError{}), ErrorKindTLS},
		{"hostname", wrap(x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}), ErrorKindTLS},
		{"cert verification", wrap(&tls.CertificateVerificationError{Err: errors.New("bad")}), ErrorKindTLS},
		{"record header", wrap(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), ErrorKindTLS},
		{"alert", wrap(tls.AlertError(42)), ErrorKindTLS},
		{"proxy connect 407", wrap(errors.New("Proxy Authentication Required")), ErrorKindProxyAuth},
		{"socks auth", wrap(errors.New("socks connect tcp 1.2.3.4:1080->x:443: username/password authentication failed")), ErrorKindProxyAuth},
		{"message dns", errors.New("lookup foo: Temporary failure in name resolution"), ErrorKindDNS},
		{"message tls", errors.New("remote error: tls: bad certificate"), ErrorKindTLS},
		{"message timeout", errors.New("operation timed out"), ErrorKindTimeout},
		{"message refused", errors.New("connectex: No connection could be made: connection refused"), ErrorKindConnRefused},
		{"message reset", errors.New("http2: server sent GOAWAY and closed the connection"), ErrorKindConnReset},
		{"other", errors.New("something else"), ErrorKindOther},
		{"timeout interface", timeoutErr{}, ErrorKindTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyError(tc.err))
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "slow" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// The classifier handles the errors real net/http clients return.
func TestClassifyErrorNetHTTP(t *testing.T) {
	ctx := context.Background()
	get := func(client *http.Client, target string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}

	t.Run("connection refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())
		require.Equal(t, ErrorKindConnRefused, ClassifyError(get(http.DefaultClient, "http://"+addr+"/")))
	})

	t.Run("untrusted certificate", func(t *testing.T) {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Config.ErrorLog = log.New(io.Discard, "", 0) // rejected handshakes are expected here
		srv.StartTLS()
		defer srv.Close()
		err := get(&http.Client{}, srv.URL)
		require.Equal(t, ErrorKindTLS, ClassifyError(err))
		require.True(t, isPermanentTransport(err))
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer srv.Close()
		defer close(release)
		err := get(&http.Client{Timeout: 20 * time.Millisecond}, srv.URL)
		require.Equal(t, ErrorKindTimeout, ClassifyError(err))
	})

	t.Run("connection closed without response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}))
		defer srv.Close()
		transport := &http.Transport{DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		err := get(&http.Client{Transport: transport}, srv.URL)
		require.Equal(t, ErrorKindConnReset, ClassifyError(err))
	})

	t.Run("proxy authentication", func(t *testing.T) {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusProxyAuthRequired)
		}))
		defer proxy.Close()
		proxyURL, err := url.Parse(proxy.URL)
		require.NoError(t, err)
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		err = get(&http.Client{Transport: transport}, "https://example.invalid/")
		require.Equal(t, ErrorKindProxyAuth, ClassifyError(err), "error: %v", err)
	})

	t.Run("proxy unreachable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())
		proxyURL := &url.URL{Scheme: "http", Host: addr}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		err = get(&http.Client{Transport: transport}, "https://example.invalid/")
		require.Equal(t, ErrorKindConnRefused, ClassifyError(err))
		require.True(t, isPreSend(err))
	})

	t.Run("plain http to tls client", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = bufio.NewReader(conn).ReadByte()
			_, _ = fmt.Fprint(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		}()
		err = get(&http.Client{}, "https://"+ln.Addr().String()+"/")
		require.Equal(t, ErrorKindTLS, ClassifyError(err), "error: %v", err)
	})
}
