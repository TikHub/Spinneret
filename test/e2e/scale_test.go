//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
)

// scenarioScaleOut (i): the replicas register as live workers in Valkey, split the report stream shards
// between them, and the load balancer spreads requests over all of them.
func scenarioScaleOut(ctx context.Context, t *testing.T, f *fixture) {
	rdb, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{f.cfg.RedisAddr}, DisableCache: true})
	require.NoError(t, err, "connect to Valkey")
	defer rdb.Close()
	p := f.cfg.RedisPrefix

	var live []string
	eventually(t, 60*time.Second, time.Second, "live workers own every shard", func() (bool, string) {
		entries, err := rdb.Do(ctx, rdb.B().Zrange().Key(p+":workers").Min("0").Max("-1").Withscores().Build()).AsZScores()
		if err != nil {
			return false, err.Error()
		}
		now := time.Now().UnixMilli()
		live = live[:0]
		for _, e := range entries {
			// Live = heartbeat within 10 s (heartbeats every 2 s).
			if now-int64(e.Score) <= 10_000 {
				live = append(live, e.Member)
			}
		}
		if len(live) != f.cfg.Replicas {
			return false, describe("live workers %v", live)
		}
		owners := map[string]int{}
		for shard := range f.cfg.ReportShards {
			owner, err := rdb.Do(ctx, rdb.B().Get().Key(fmt.Sprintf("%s:{r%d}:owner", p, shard)).Build()).ToString()
			if err != nil {
				return false, describe("shard %d owner: %v", shard, err)
			}
			owners[owner]++
		}
		for owner := range owners {
			if !containsString(live, owner) {
				return false, describe("shard owner %s is not a live worker (%v)", owner, owners)
			}
		}
		// Ownership target: ceil(shards / live) each, so every live worker owns shards.
		share := (f.cfg.ReportShards + len(live) - 1) / len(live)
		for _, w := range live {
			if owners[w] == 0 || owners[w] > share {
				return false, describe("unbalanced shard ownership %v (share %d)", owners, share)
			}
		}
		return true, describe("owners %v", owners)
	})
	t.Logf("live workers: %v", live)

	// Each replica has its own process start time, so /metrics identifies the replica that answered.
	// The requests are concurrent: the load balancer (least_conn) sends a sequential series to the
	// first replica that has no in-flight request, which would say nothing about its distribution.
	client := &http.Client{Timeout: 15 * time.Second}
	starts := map[string]int{}
	eventually(t, 30*time.Second, time.Second, "every replica answers through the load balancer", func() (bool, string) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		seen := map[string]int{}
		var failed error
		for range 4 * f.cfg.Replicas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := processStart(ctx, client, f.cfg.URL+"/metrics")
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failed = err
					return
				}
				seen[v]++
			}()
		}
		wg.Wait()
		if failed != nil {
			return false, failed.Error()
		}
		for v, n := range seen {
			starts[v] += n
		}
		return len(starts) >= f.cfg.Replicas, describe("%v", starts)
	})
	t.Logf("process_start_time_seconds seen through the load balancer: %v", starts)
	require.Len(t, starts, f.cfg.Replicas, "the load balancer must route to every replica")
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// processStart returns the process_start_time_seconds sample of a /metrics response on a fresh
// connection (keep-alive would pin the backend).
func processStart(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /metrics: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("GET /metrics: status %d: %s", resp.StatusCode, b)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "process_start_time_seconds "); ok {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return "", fmt.Errorf("parse process_start_time_seconds %q: %w", v, err)
			}
			return v, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read /metrics: %w", err)
	}
	return "", errors.New("process_start_time_seconds not found in /metrics")
}
