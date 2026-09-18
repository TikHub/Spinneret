//go:build perf

package perf

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

var (
	flagURL       = flag.String("perf.url", "", "Redis/Valkey URL (default: $SPINNERET_TEST_REDIS_URL)")
	flagBig       = flag.Bool("perf.big", false, "seed 100k identities x 50 groups instead of 20k x 5 (slow)")
	flagOps       = flag.Int("perf.ops", 2000, "measured calls per block")
	flagChunk     = flag.Int("perf.chunk", 250, "calls between two dataset-restoring maintain() passes")
	flagSlowlog   = flag.Bool("perf.slowlog", true, "collect per-call server durations with SLOWLOG threshold 0")
	flagKeep      = flag.Bool("perf.keep", false, "keep the seeded keys after the run (for manual inspection)")
	flagIOThreads = flag.Bool("perf.iothreads", false, "run the throwaway-container io-threads comparison (needs docker)")
)

// slowlogOn mirrors *flagSlowlog for the measurement helpers.
var slowlogOn = true

// Process-wide environment and dataset: seeding 100k x 50 costs minutes, so it
// happens once per binary and is dropped when the binary exits.
var (
	sharedEnv *env
	sharedDS  *dataset
)

// slowlogSaved holds the SLOWLOG configuration replaced for the run.
var slowlogSaved [2]string

func TestMain(m *testing.M) {
	flag.Parse()
	slowlogOn = *flagSlowlog

	url := *flagURL
	if url == "" {
		url = os.Getenv(redisURLEnv)
	}
	if url == "" {
		fmt.Fprintf(os.Stderr, "perf: %s is not set; every benchmark will skip\n", redisURLEnv)
		os.Exit(m.Run())
	}

	ctx := context.Background()
	c, err := storeredis.Open(ctx, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "perf: connect %s: %v\n", url, err)
		os.Exit(1)
	}
	sharedEnv = &env{client: c, ctx: ctx}

	// The SLOWLOG configuration is saved and restored unconditionally: a
	// benchmark that lowers the threshold and then fails must not leave the
	// shared Valkey logging every command.
	boot := &bootT{}
	slowlogSaved[0] = sharedEnv.configGet(boot, "slowlog-log-slower-than")
	slowlogSaved[1] = sharedEnv.configGet(boot, "slowlog-max-len")
	if slowlogOn {
		sharedEnv.rawConfigSet(boot, "slowlog-max-len", "20000")
		sharedEnv.rawConfigSet(boot, "slowlog-log-slower-than", "0")
	}

	identities, groups := int64(20_000), 5
	if *flagBig {
		identities, groups = 100_000, 50
	}
	start := time.Now()
	sharedDS = newDataset(boot, sharedEnv, identities, groups, true, false)
	fmt.Fprintf(os.Stderr, "perf: dataset %s ready in %s\n", sharedDS.prefix, time.Since(start).Round(time.Millisecond))

	code := m.Run()

	sharedEnv.rawConfigSet(boot, "slowlog-log-slower-than", slowlogSaved[0])
	sharedEnv.rawConfigSet(boot, "slowlog-max-len", slowlogSaved[1])
	_ = c.Do(ctx, c.B().SlowlogReset().Build()).Error()
	if !*flagKeep {
		sharedDS.drop(sharedEnv)
	}
	c.Close()
	os.Exit(code)
}

// benchEnv returns the process-wide environment and dataset.
func benchEnv(tb testing.TB) (*env, *dataset) {
	tb.Helper()
	if sharedEnv == nil {
		tb.Skipf("set %s to run the perf harness", redisURLEnv)
	}
	return sharedEnv, sharedDS
}

// bootT is a testing.TB stand-in used during TestMain, where no test is
// running yet. Only the failure and logging methods the harness calls are
// implemented; a failure aborts the process, which is what a broken
// environment should do before any benchmark reports a number.
type bootT struct{ testing.T }

func (b *bootT) Helper() {}
func (b *bootT) Log(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
}

func (b *bootT) Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func (b *bootT) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "perf setup: "+format+"\n", args...)
	os.Exit(1)
}

func (b *bootT) Cleanup(func()) {}
func (b *bootT) Skipf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "perf setup skip: "+format+"\n", args...)
	os.Exit(0)
}
