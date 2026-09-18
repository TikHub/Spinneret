package spinneret

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

var (
	proxyAuthHints = []string{
		"proxy authentication required",
		"username/password authentication failed",
		"socks authentication failed",
	}
	dnsHints = []string{
		"no such host",
		"name or service not known",
		"nodename nor servname",
		"temporary failure in name resolution",
		"server misbehaving",
		"no address associated with hostname",
		"name resolution",
		"failed to resolve",
	}
	tlsHints = []string{
		"tls:", "x509:", "certificate", "handshake failure", "first record does not look like a tls handshake",
		"server gave http response to https client",
	}
	timeoutHints = []string{
		"timeout", "timed out", "deadline exceeded",
	}
	refusedHints = []string{
		"connection refused", "no route to host", "network is unreachable", "host is down",
	}
	resetHints = []string{
		"connection reset", "reset by peer", "broken pipe", "server closed", "peer closed",
		"connection aborted", "unexpected eof", "use of closed network connection",
		"http2: server sent goaway", "stream error", "transport connection broken",
	}
)

// ClassifyError maps a request error to a report error kind: "timeout",
// "conn_reset", "conn_refused", "proxy_auth", "tls", "dns" or "other"; ""
// for a nil error.
//
// It understands the errors returned by net/http clients (*url.Error and the
// net, syscall, crypto/tls and crypto/x509 errors they wrap, including proxy
// CONNECT and SOCKS5 failures) and falls back to the error message for other
// errors. A 407 response is not an error in net/http: report its status with
// [Lease.ReportResponse], which sets "proxy_auth" automatically.
func ClassifyError(err error) string {
	if err == nil {
		return ErrorKindNone
	}
	message := strings.ToLower(err.Error())
	if containsAny(message, proxyAuthHints) {
		return ErrorKindProxyAuth
	}
	if isCertificateError(err) || isTLSError(err) {
		return ErrorKindTLS
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return ErrorKindDNS
	}
	if isTimeout(err) {
		return ErrorKindTimeout
	}
	if kind := classifyErrno(err); kind != "" {
		return kind
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return ErrorKindConnReset
	}
	return classifyMessage(message)
}

func classifyMessage(message string) string {
	for _, rule := range []struct {
		hints []string
		kind  string
	}{
		{dnsHints, ErrorKindDNS},
		{tlsHints, ErrorKindTLS},
		{timeoutHints, ErrorKindTimeout},
		{refusedHints, ErrorKindConnRefused},
		{resetHints, ErrorKindConnReset},
	} {
		if containsAny(message, rule.hints) {
			return rule.kind
		}
	}
	return ErrorKindOther
}

func containsAny(s string, hints []string) bool {
	for _, hint := range hints {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func classifyErrno(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH):
		return ErrorKindConnRefused
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.EPIPE):
		return ErrorKindConnReset
	default:
		return ""
	}
}

func isCertificateError(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &invalid)
}

func isTLSError(err error) bool {
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var echRejection *tls.ECHRejectionError
	return errors.As(err, &record) || errors.As(err, &alert) || errors.As(err, &echRejection)
}
