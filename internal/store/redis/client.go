// Package redis provides the Redis/Valkey building blocks shared by every
// hot-state component: the rueidis client factory, the frozen key schema
// (Keys), the Lua script loader that prepends the common prelude
// (lua/common.lua), and small distributed lock helpers.
package redis

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

const (
	// ClientName is reported to the server via CLIENT SETNAME on every connection.
	ClientName = "spinneret"

	defaultDialTimeout      = 5 * time.Second
	defaultConnWriteTimeout = 10 * time.Second
)

// Open creates a rueidis client and verifies connectivity with PING.
//
// url is a redis:// or rediss:// URL (credentials, TLS and database number are
// taken from it). When clusterAddrs is non-empty the client connects to those
// seed nodes in cluster mode; the URL, if any, then only contributes
// credentials, TLS and connection options. Client-side caching is disabled.
func Open(ctx context.Context, url string, clusterAddrs []string) (rueidis.Client, error) {
	opt, err := clientOption(url, clusterAddrs)
	if err != nil {
		return nil, err
	}
	client, err := rueidis.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("redis: connect: %w", err)
	}
	if err := client.Do(ctx, client.B().Ping().Build()).Error(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return client, nil
}

// clientOption builds the rueidis options for Open without dialing.
func clientOption(rawURL string, clusterAddrs []string) (rueidis.ClientOption, error) {
	addrs := cleanAddrs(clusterAddrs)
	if strings.TrimSpace(rawURL) == "" && len(addrs) == 0 {
		return rueidis.ClientOption{}, errors.New("redis: url or cluster addresses required")
	}

	var opt rueidis.ClientOption
	if strings.TrimSpace(rawURL) != "" {
		parsed, err := rueidis.ParseURL(strings.TrimSpace(rawURL))
		if err != nil {
			return rueidis.ClientOption{}, fmt.Errorf("redis: parse url: %w", sanitizeURLError(err))
		}
		opt = parsed
	}

	if len(addrs) > 0 {
		if opt.SelectDB != 0 {
			return rueidis.ClientOption{}, fmt.Errorf("redis: database %d cannot be selected in cluster mode", opt.SelectDB)
		}
		opt.InitAddress = addrs
		opt.ShuffleInit = true
		if opt.TLSConfig != nil {
			// The server name derived from the URL host does not apply to every seed node.
			opt.TLSConfig.ServerName = ""
		}
	}

	if opt.ClientName == "" {
		opt.ClientName = ClientName
	}
	if opt.Dialer.Timeout <= 0 {
		opt.Dialer.Timeout = defaultDialTimeout
	}
	if opt.ConnWriteTimeout <= 0 {
		opt.ConnWriteTimeout = defaultConnWriteTimeout
	}
	opt.DisableCache = true
	return opt, nil
}

// cleanAddrs trims the given addresses and removes empty entries.
func cleanAddrs(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// sanitizeURLError strips the raw URL (which may contain a password) from
// net/url parse errors.
func sanitizeURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}
