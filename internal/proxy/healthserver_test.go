package proxy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testHTTPProxy is a forwarding HTTP proxy supporting absolute-form requests
// and CONNECT tunnels, with optional Basic proxy authentication.
type testHTTPProxy struct {
	*httptest.Server
	user, pass string
	requests   atomic.Int64
	connects   atomic.Int64
	upstream   *http.Transport
}

func newTestHTTPProxy(t *testing.T, user, pass string, useTLS bool) *testHTTPProxy {
	t.Helper()
	p := &testHTTPProxy{user: user, pass: pass, upstream: &http.Transport{}}
	if useTLS {
		p.Server = httptest.NewTLSServer(p)
	} else {
		p.Server = httptest.NewServer(p)
	}
	t.Cleanup(func() {
		p.Close()
		p.upstream.CloseIdleConnections()
	})
	return p
}

func (p *testHTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.user != "" {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.user+":"+p.pass))
		if r.Header.Get("Proxy-Authorization") != want {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
	}
	if r.Method == http.MethodConnect {
		p.connects.Add(1)
		p.tunnel(w, r)
		return
	}
	p.requests.Add(1)
	if !r.URL.IsAbs() {
		http.Error(w, "absolute-form request expected", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	resp, err := p.upstream.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *testHTTPProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	target, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
	if err != nil {
		http.Error(w, "dial failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = target.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		_ = target.Close()
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = conn.Close()
		_ = target.Close()
		return
	}
	pipeConns(conn, target)
}

// pipeConns copies in both directions until either side closes.
func pipeConns(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	go func() {
		_, _ = io.Copy(a, b)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(b, a)
		closeBoth()
	}()
}

// testSOCKS5 is a minimal RFC 1928/1929 SOCKS5 server supporting CONNECT.
type testSOCKS5 struct {
	ln         net.Listener
	user, pass string
	conns      atomic.Int64
}

func newTestSOCKS5(t *testing.T, user, pass string) *testSOCKS5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &testSOCKS5{ln: ln, user: user, pass: pass}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testSOCKS5) addr() string { return s.ln.Addr().String() }

func (s *testSOCKS5) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *testSOCKS5) handle(c net.Conn) {
	s.conns.Add(1)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	target, ok := s.negotiate(c)
	if !ok {
		_ = c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	pipeConns(c, target)
}

func (s *testSOCKS5) negotiate(c net.Conn) (net.Conn, bool) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return nil, false
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return nil, false
	}
	method := byte(0x00)
	if s.user != "" {
		method = 0x02
	}
	if !bytes.Contains(methods, []byte{method}) {
		_, _ = c.Write([]byte{5, 0xff})
		return nil, false
	}
	if _, err := c.Write([]byte{5, method}); err != nil {
		return nil, false
	}
	if method == 0x02 && !s.authenticate(c) {
		return nil, false
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[1] != 1 {
		return nil, false
	}
	var host string
	switch req[3] {
	case 1, 4:
		size := 4
		if req[3] == 4 {
			size = 16
		}
		addr := make([]byte, size)
		if _, err := io.ReadFull(c, addr); err != nil {
			return nil, false
		}
		host = net.IP(addr).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return nil, false
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return nil, false
		}
		host = string(name)
	default:
		return nil, false
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(c, portBytes); err != nil {
		return nil, false
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, false
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		_ = target.Close()
		return nil, false
	}
	return target, true
}

func (s *testSOCKS5) authenticate(c net.Conn) bool {
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || b[0] != 1 {
		return false
	}
	user := make([]byte, b[1])
	if _, err := io.ReadFull(c, user); err != nil {
		return false
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(c, pl); err != nil {
		return false
	}
	pass := make([]byte, pl[0])
	if _, err := io.ReadFull(c, pass); err != nil {
		return false
	}
	if string(user) != s.user || string(pass) != s.pass {
		_, _ = c.Write([]byte{1, 1})
		return false
	}
	_, err := c.Write([]byte{1, 0})
	return err == nil
}

// newCheckTarget serves the check and exit IP endpoints.
func newCheckTarget(t *testing.T, useTLS bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/check", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/ip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.9"}`))
	})
	mux.HandleFunc("/ip-text", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("198.51.100.1\n")) })
	mux.HandleFunc("/ip-bad", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not an ip")) })
	mux.HandleFunc("/unavailable", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	var srv *httptest.Server
	if useTLS {
		srv = httptest.NewTLSServer(mux)
	} else {
		srv = httptest.NewServer(mux)
	}
	t.Cleanup(srv.Close)
	return srv
}

// testTLSConfig trusts the certificates of the given httptest TLS servers.
func testTLSConfig(servers ...*httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	for _, s := range servers {
		pool.AddCert(s.Certificate())
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// closedAddr returns a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// writeTestGeoDB writes a MaxMind DB (IPv4, 24-bit records, one node) that maps
// every address to {"country":{"iso_code":"JP"},"city":{"names":{"en":"Tokyo"}}}.
func writeTestGeoDB(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	mapHdr := func(b *bytes.Buffer, n int) { b.WriteByte(0xE0 | byte(n)) }
	str := func(b *bytes.Buffer, s string) {
		b.WriteByte(0x40 | byte(len(s)))
		b.WriteString(s)
	}
	mapHdr(&data, 2)
	str(&data, "country")
	mapHdr(&data, 1)
	str(&data, "iso_code")
	str(&data, "JP")
	str(&data, "city")
	mapHdr(&data, 1)
	str(&data, "names")
	mapHdr(&data, 1)
	str(&data, "en")
	str(&data, "Tokyo")

	var meta bytes.Buffer
	uint16v := func(v byte) { meta.Write([]byte{0xA1, v}) }
	mapHdr(&meta, 9)
	str(&meta, "node_count")
	meta.Write([]byte{0xC1, 1})
	str(&meta, "record_size")
	uint16v(24)
	str(&meta, "ip_version")
	uint16v(4)
	str(&meta, "database_type")
	str(&meta, "Test-City")
	str(&meta, "languages")
	meta.Write([]byte{0x01, 0x04})
	str(&meta, "en")
	str(&meta, "binary_format_major_version")
	uint16v(2)
	str(&meta, "binary_format_minor_version")
	meta.WriteByte(0xA0)
	str(&meta, "build_epoch")
	meta.Write([]byte{0x01, 0x02, 0x01})
	str(&meta, "description")
	mapHdr(&meta, 1)
	str(&meta, "en")
	str(&meta, "test")

	var file bytes.Buffer
	file.Write([]byte{0, 0, 17, 0, 0, 17}) // both records point to data offset 0 (node_count + 16)
	file.Write(make([]byte, 16))
	file.Write(data.Bytes())
	file.WriteString("\xAB\xCD\xEFMaxMind.com")
	file.Write(meta.Bytes())

	path := filepath.Join(t.TempDir(), "test.mmdb")
	require.NoError(t, os.WriteFile(path, file.Bytes(), 0o600))
	return path
}
