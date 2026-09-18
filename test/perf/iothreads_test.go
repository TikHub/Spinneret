//go:build perf

package perf

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

const (
	throwawayName  = "spinneret-perf-valkey"
	throwawayPort  = 46999
	throwawayImage = "valkey/valkey:8-alpine"
)

// BenchmarkIOThreads measures whether Valkey's io-threads raise the acquire
// ceiling. The command itself always executes on the main thread; io-threads
// only move socket reads/writes and protocol parsing off it. The benchmark runs
// a closed-loop concurrent acquire load against a throwaway container so the
// shared infrastructure is never reconfigured.
//
// It needs docker and the -perf.iothreads flag.
func BenchmarkIOThreads(b *testing.B) {
	if !*flagIOThreads {
		b.Skip("pass -perf.iothreads to run the throwaway-container comparison (needs docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		b.Skip("docker not found")
	}

	type variant struct {
		label string
		args  []string
	}
	variants := []variant{
		{"io-threads=1, appendonly no", []string{"--io-threads", "1", "--save", "", "--appendonly", "no"}},
		{"io-threads=4, appendonly no", []string{"--io-threads", "4", "--save", "", "--appendonly", "no"}},
		{"io-threads=1, appendonly yes (deployed config)",
			[]string{"--io-threads", "1", "--save", "", "--appendonly", "yes", "--appendfsync", "everysec"}},
		{"io-threads=4, appendonly yes",
			[]string{"--io-threads", "4", "--save", "", "--appendonly", "yes", "--appendfsync", "everysec"}},
	}

	var results []ioThreadsResult

	for _, v := range variants {
		func() {
			removeThrowaway()
			startThrowaway(b, v.args)
			defer removeThrowaway()

			url := fmt.Sprintf("redis://127.0.0.1:%d/0", throwawayPort)
			e := &env{ctx: context.Background()}
			var err error
			for attempt := 0; attempt < 40; attempt++ {
				e.client, err = storeredis.Open(e.ctx, url, nil)
				if err == nil {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if err != nil {
				b.Fatalf("connect throwaway: %v", err)
			}
			defer e.client.Close()

			ds := newDataset(b, e, 20_000, 5, false, false)
			s := scripts(b)
			warm(b, e, s.acquire)

			cfg := defaultAcquireCfg()
			// A pool that does not drain under a closed loop: an identity whose
			// concurrency limit is far away keeps its ready score at "now".
			cfg.maxLeases = 100_000

			for _, clients := range []int{1, 8, 32} {
				r := ioThreadsRun(b, e, ds, s, cfg, clients, 3*time.Second)
				r.label = v.label
				r.clients = clients
				results = append(results, r)
			}
		}()
	}

	logBoth(b, fmt.Sprintf("\n%-46s %8s %12s %10s %8s %9s %9s",
		"valkey configuration", "clients", "acquires/s", "us/call", "cores", "wall p50", "wall p99"))
	for _, r := range results {
		logBoth(b, fmt.Sprintf("%-46s %8d %12.0f %10.1f %8.2f %9s %9s",
			r.label, r.clients, r.rate, r.serverPer, r.cpuCores, r.p50.Round(time.Microsecond), r.p99.Round(time.Microsecond)))
	}
}

// ioThreadsResult is one row of the io-threads comparison.
type ioThreadsResult struct {
	label     string
	clients   int
	rate      float64
	serverPer float64
	cpuCores  float64
	p50, p99  time.Duration
}

// ioThreadsRun drives a closed-loop acquire load for d with the given number of
// concurrent callers and returns the achieved rate and the server-side cost.
func ioThreadsRun(b *testing.B, e *env, ds *dataset, s *scriptSet, cfg acquireCfg, clients int, d time.Duration) ioThreadsResult {
	argv := make([][][]string, clients)
	const perClient = 256
	for c := range argv {
		argv[c] = make([][]string, perClient)
		for i := range argv[c] {
			argv[c][i] = ds.acquireArgs(cfg, ds.epoch, c*perClient+i)
		}
	}

	beforeStats := e.commandStats(b)
	beforeCPU := e.cpuSeconds(b)
	var (
		ops      atomic.Int64
		failures atomic.Int64
		mu       sync.Mutex
		lat      []time.Duration
		wg       sync.WaitGroup
	)
	deadline := time.Now().Add(d)
	start := time.Now()
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			local := make([]time.Duration, 0, 1<<14)
			for i := 0; time.Now().Before(deadline); i++ {
				t0 := time.Now()
				res := s.acquire.Exec(e.ctx, e.client, []string{ds.meta()}, argv[c][i%perClient])
				local = append(local, time.Since(t0))
				if res.Error() != nil {
					failures.Add(1)
					continue
				}
				ops.Add(1)
			}
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	elapsed := time.Since(start)
	afterStats := e.commandStats(b)
	afterCPU := e.cpuSeconds(b)

	if failures.Load() > 0 {
		b.Errorf("io-threads run: %d failures", failures.Load())
	}
	delta := diffStats(beforeStats, afterStats)
	ev := delta["evalsha"]
	per := 0.0
	if ev.calls > 0 {
		per = ev.usec / float64(ev.calls)
	}
	sm := sample{wall: lat}
	return ioThreadsResult{
		rate:      float64(ops.Load()) / elapsed.Seconds(),
		serverPer: per,
		cpuCores:  (afterCPU - beforeCPU) / elapsed.Seconds(),
		p50:       sm.pct(0.50),
		p99:       sm.pct(0.99),
	}
}

// cpuSeconds returns used_cpu_user + used_cpu_sys from INFO cpu.
func (e *env) cpuSeconds(tb testing.TB) float64 {
	tb.Helper()
	info, err := e.client.Do(e.ctx, e.client.B().Info().Section("cpu").Build()).ToString()
	if err != nil {
		tb.Fatalf("info cpu: %v", err)
	}
	var total float64
	for _, line := range strings.Split(info, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if k == "used_cpu_user" || k == "used_cpu_sys" {
			f, _ := strconv.ParseFloat(v, 64)
			total += f
		}
	}
	return total
}

// startThrowaway runs a disposable Valkey with the given server arguments.
func startThrowaway(b *testing.B, args []string) {
	b.Helper()
	full := append([]string{
		"run", "-d", "--name", throwawayName,
		"-p", fmt.Sprintf("127.0.0.1:%d:6379", throwawayPort),
		throwawayImage, "valkey-server",
	}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		b.Fatalf("docker run: %v: %s", err, out)
	}
}

// removeThrowaway deletes the disposable container if it exists.
func removeThrowaway() {
	_ = exec.Command("docker", "rm", "-f", throwawayName).Run()
}
