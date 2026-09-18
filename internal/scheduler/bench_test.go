package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
)

const (
	benchEnv        = "SPINNERET_BENCH"
	benchIdentities = 100_000
	benchSeedBatch  = 2000
)

// benchService seeds benchIdentities identities into one endpoint group and
// returns a scheduler using the real clock and random source.
func benchService(tb testing.TB) (*Service, context.Context) {
	tb.Helper()
	if os.Getenv(benchEnv) != "1" {
		tb.Skipf("set %s=1 to run the acquire benchmark", benchEnv)
	}
	f := newFixture(tb)
	rot := f.rotation(f.web)
	rot.Rotation.CandidateSample = 32

	cmds := make(rueidis.Commands, 0, 2*benchSeedBatch)
	flush := func() {
		for _, res := range f.rdb.DoMulti(f.ctx, cmds...) {
			require.NoError(tb, res.Error())
		}
		cmds = cmds[:0]
	}
	for i := int64(1); i <= benchIdentities; i++ {
		id := strconv.FormatInt(i, 10)
		cmds = append(cmds,
			f.rdb.B().Hset().Key(f.keys.Identity(testSiteKey, i)).FieldValue().
				FieldValue("iid", "idt_"+id).FieldValue("st", "active").FieldValue("ty", testTypeName).
				FieldValue("tv", "1").FieldValue("pv", "1").FieldValue("al", "0").FieldValue("acc", "").Build(),
			f.rdb.B().Zadd().Key(f.keys.Ready(testSiteKey, testWebGroup)).ScoreMember().
				ScoreMember(float64(i%60000), id).Build(),
		)
		if len(cmds) >= 2*benchSeedBatch {
			flush()
		}
	}
	flush()
	f.svc.clock = time.Now
	f.svc.random = rand.Float64
	return f.svc, f.tokenCtx()
}

// TestAcquireThroughput measures parallel Acquire+Release against the test
// Redis. It runs only with SPINNERET_BENCH=1; SPINNERET_BENCH_WORKERS and
// SPINNERET_BENCH_DURATION tune the load.
func TestAcquireThroughput(t *testing.T) {
	svc, ctx := benchService(t)
	workers := 64
	if v, err := strconv.Atoi(os.Getenv("SPINNERET_BENCH_WORKERS")); err == nil && v > 0 {
		workers = v
	}
	duration := 10 * time.Second
	if v, err := time.ParseDuration(os.Getenv("SPINNERET_BENCH_DURATION")); err == nil && v > 0 {
		duration = v
	}
	// SPINNERET_BENCH_RATE paces the total acquire rate (0 = closed loop).
	var pace time.Duration
	if v, err := strconv.Atoi(os.Getenv("SPINNERET_BENCH_RATE")); err == nil && v > 0 {
		pace = time.Duration(int64(workers) * int64(time.Second) / int64(v))
	}

	var (
		ops      atomic.Int64
		failures atomic.Int64
		mu       sync.Mutex
		acquireL []time.Duration
		releaseL []time.Duration
		wg       sync.WaitGroup
	)
	before := scriptStats(t, svc)
	start := time.Now()
	deadline := start.Add(duration)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			la := make([]time.Duration, 0, 1<<16)
			lr := make([]time.Duration, 0, 1<<16)
			next := time.Now().Add(time.Duration(rand.Int64N(int64(pace) + 1)))
			for time.Now().Before(deadline) {
				if pace > 0 {
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					}
					next = next.Add(pace)
				}
				t0 := time.Now()
				g, err := svc.Acquire(ctx, AcquireRequest{Site: "demo", Client: "web"})
				la = append(la, time.Since(t0))
				if err != nil {
					failures.Add(1)
					continue
				}
				ops.Add(1)
				t1 := time.Now()
				if _, err := svc.Release(ctx, g.Lease.ID); err != nil {
					failures.Add(1)
				}
				lr = append(lr, time.Since(t1))
			}
			mu.Lock()
			acquireL = append(acquireL, la...)
			releaseL = append(releaseL, lr...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	after := scriptStats(t, svc)
	calls, usec := after[0]-before[0], after[1]-before[1]
	perCall := 0.0
	if calls > 0 {
		perCall = usec / calls
	}
	report := fmt.Sprintf("acquire+release identities=%d K=32 workers=%d pace=%s GOMAXPROCS=%d elapsed=%s acquires=%d failures=%d rate=%.0f acquires/s | acquire %s | release %s | server evalsha calls=%.0f avg=%.0fus (all clients)",
		benchIdentities, workers, pace, runtime.GOMAXPROCS(0), elapsed.Round(time.Millisecond), ops.Load(), failures.Load(),
		float64(ops.Load())/elapsed.Seconds(), percentiles(acquireL), percentiles(releaseL), calls, perCall)
	t.Log(report)
	fmt.Fprintln(os.Stderr, report)
	require.Zero(t, failures.Load())
}

// scriptStats returns the server's cumulative EVALSHA calls and microseconds.
func scriptStats(t *testing.T, svc *Service) [2]float64 {
	t.Helper()
	info, err := svc.rdb.Do(context.Background(), svc.rdb.B().Info().Section("commandstats").Build()).ToString()
	require.NoError(t, err)
	var out [2]float64
	for _, line := range strings.Split(info, "\n") {
		if !strings.HasPrefix(line, "cmdstat_evalsha:") {
			continue
		}
		for _, kv := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "cmdstat_evalsha:")), ",") {
			k, v, _ := strings.Cut(kv, "=")
			n, _ := strconv.ParseFloat(v, 64)
			switch k {
			case "calls":
				out[0] = n
			case "usec":
				out[1] = n
			}
		}
	}
	return out
}

func percentiles(l []time.Duration) string {
	if len(l) == 0 {
		return "no samples"
	}
	slices.Sort(l)
	at := func(p float64) time.Duration { return l[min(len(l)-1, int(float64(len(l))*p))] }
	return fmt.Sprintf("p50=%s p90=%s p99=%s p99.9=%s max=%s", at(0.5), at(0.9), at(0.99), at(0.999), l[len(l)-1])
}

func BenchmarkAcquireRelease(b *testing.B) {
	svc, ctx := benchService(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			g, err := svc.Acquire(ctx, AcquireRequest{Site: "demo", Client: "web"})
			if err != nil {
				b.Error(err)
				return
			}
			if _, err := svc.Release(ctx, g.Lease.ID); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
