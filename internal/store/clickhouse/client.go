// Package clickhouse stores raw per-request events (report_events,
// lease_events) in ClickHouse: connection factory, idempotent schema
// migration and an asynchronous, bounded batch writer. ClickHouse is optional;
// a nil connection turns the writer into a no-op.
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/Evil0ctal/Spinneret/internal/version"
)

const (
	defaultDialTimeout = 10 * time.Second
	productName        = "spinneret"
)

// ErrNoURL is returned by Open when no ClickHouse URL is configured.
var ErrNoURL = errors.New("clickhouse: url is empty")

// Open connects to ClickHouse over the native protocol and verifies the
// connection with a ping. url uses the clickhouse-go DSN format, e.g.
// "clickhouse://user:pass@host:9000/db?dial_timeout=5s". LZ4 compression and
// a 10 s dial timeout are applied unless the URL overrides them.
func Open(ctx context.Context, url string) (chdriver.Conn, error) {
	opt, err := options(url)
	if err != nil {
		return nil, err
	}
	conn, err := clickhouse.Open(opt)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}
	return conn, nil
}

// options parses url into driver options without dialing.
func options(rawURL string) (*clickhouse.Options, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, ErrNoURL
	}
	opt, err := clickhouse.ParseDSN(rawURL)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: parse url: %w", sanitizeURLError(err))
	}
	if opt.Protocol != clickhouse.Native {
		return nil, errors.New("clickhouse: only the native protocol is supported (use clickhouse:// or tcp://)")
	}
	if opt.Compression == nil {
		opt.Compression = &clickhouse.Compression{Method: clickhouse.CompressionLZ4}
	}
	if opt.DialTimeout <= 0 {
		opt.DialTimeout = defaultDialTimeout
	}
	opt.ClientInfo.Products = append(opt.ClientInfo.Products, struct {
		Name    string
		Version string
	}{Name: productName, Version: version.String()})
	return opt, nil
}

// sanitizeURLError strips the raw URL (which may contain a password) from URL parse errors.
func sanitizeURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}
