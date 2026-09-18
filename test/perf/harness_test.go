//go:build perf

package perf

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"

	storeredis "github.com/TikHub/Spinneret/internal/store/redis"
)

// redisURLEnv names the Valkey the harness runs against.
const redisURLEnv = "SPINNERET_TEST_REDIS_URL"

// cmdStat is one INFO commandstats row.
type cmdStat struct {
	calls int64
	usec  float64
}

// env is the shared connection and dataset of one benchmark process.
type env struct {
	client rueidis.Client
	keys   storeredis.Keys
	ctx    context.Context
	ds     *dataset
}

// openEnv connects to the Valkey named by SPINNERET_TEST_REDIS_URL.
func openEnv(tb testing.TB, url string) *env {
	tb.Helper()
	if url == "" {
		url = os.Getenv(redisURLEnv)
	}
	if url == "" {
		tb.Skipf("set %s to run the perf harness", redisURLEnv)
	}
	ctx := context.Background()
	c, err := storeredis.Open(ctx, url, nil)
	if err != nil {
		tb.Fatalf("connect %s: %v", url, err)
	}
	tb.Cleanup(c.Close)
	return &env{client: c, ctx: ctx}
}

// do runs one command and fails the benchmark when it errors.
func (e *env) do(tb testing.TB, cmd rueidis.Completed) rueidis.RedisResult {
	tb.Helper()
	res := e.client.Do(e.ctx, cmd)
	if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
		tb.Fatalf("%v: %v", cmd.Commands(), err)
	}
	return res
}

// commandStats returns the INFO commandstats table.
func (e *env) commandStats(tb testing.TB) map[string]cmdStat {
	tb.Helper()
	info, err := e.client.Do(e.ctx, e.client.B().Info().Section("commandstats").Build()).ToString()
	if err != nil {
		tb.Fatalf("info commandstats: %v", err)
	}
	out := make(map[string]cmdStat, 64)
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "cmdstat_") {
			continue
		}
		name, rest, ok := strings.Cut(strings.TrimPrefix(line, "cmdstat_"), ":")
		if !ok {
			continue
		}
		var st cmdStat
		for _, kv := range strings.Split(rest, ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "calls":
				st.calls, _ = strconv.ParseInt(v, 10, 64)
			case "usec":
				st.usec, _ = strconv.ParseFloat(v, 64)
			}
		}
		out[name] = st
	}
	return out
}

// diffStats subtracts before from after, dropping rows that did not move.
func diffStats(before, after map[string]cmdStat) map[string]cmdStat {
	out := make(map[string]cmdStat, len(after))
	for name, a := range after {
		b := before[name]
		if d := (cmdStat{calls: a.calls - b.calls, usec: a.usec - b.usec}); d.calls > 0 {
			out[name] = d
		}
	}
	return out
}

// configGet reads one CONFIG parameter.
func (e *env) configGet(tb testing.TB, param string) string {
	tb.Helper()
	m, err := e.client.Do(e.ctx, e.client.B().ConfigGet().Parameter(param).Build()).AsStrMap()
	if err != nil {
		tb.Fatalf("config get %s: %v", param, err)
	}
	return m[param]
}

// rawConfigSet writes one CONFIG parameter without registering a restore.
func (e *env) rawConfigSet(tb testing.TB, param, value string) {
	tb.Helper()
	if err := e.client.Do(e.ctx, e.client.B().ConfigSet().ParameterValue().ParameterValue(param, value).Build()).Error(); err != nil {
		tb.Fatalf("config set %s=%s: %v", param, value, err)
	}
}

// configSet writes one CONFIG parameter and restores it at the end of the test.
func (e *env) configSet(tb testing.TB, param, value string) {
	tb.Helper()
	old := e.configGet(tb, param)
	if err := e.client.Do(e.ctx, e.client.B().ConfigSet().ParameterValue().ParameterValue(param, value).Build()).Error(); err != nil {
		tb.Fatalf("config set %s=%s: %v", param, value, err)
	}
	tb.Cleanup(func() {
		_ = e.client.Do(e.ctx, e.client.B().ConfigSet().ParameterValue().ParameterValue(param, old).Build()).Error()
	})
}

// slowlogEntry is one SLOWLOG GET row reduced to what the harness needs.
type slowlogEntry struct {
	usec int64
	cmd  string
}

// slowlogGet reads up to n entries, newest first.
func (e *env) slowlogGet(tb testing.TB, n int) []slowlogEntry {
	tb.Helper()
	msgs, err := e.client.Do(e.ctx, e.client.B().SlowlogGet().Count(int64(n)).Build()).ToArray()
	if err != nil {
		tb.Fatalf("slowlog get: %v", err)
	}
	out := make([]slowlogEntry, 0, len(msgs))
	for i := range msgs {
		row, err := msgs[i].ToArray()
		if err != nil || len(row) < 4 {
			continue
		}
		usec, _ := row[2].AsInt64()
		argv, _ := row[3].ToArray()
		cmd := ""
		if len(argv) > 0 {
			cmd, _ = argv[0].ToString()
		}
		out = append(out, slowlogEntry{usec: usec, cmd: strings.ToUpper(cmd)})
	}
	return out
}

// sample is the result of one measured block.
type sample struct {
	name string
	ops  int

	wall []time.Duration // per-call wall clock (client side)

	evalCalls int64   // EVALSHA/FCALL calls the server counted
	evalUsec  float64 // server microseconds of those calls
	nested    map[string]cmdStat

	slowP50 float64 // server microseconds per call, from SLOWLOG
	slowP99 float64
	slowN   int
}

// serverPerOp is the mean server-side microseconds of one script call.
func (s sample) serverPerOp() float64 {
	if s.evalCalls == 0 {
		return 0
	}
	return s.evalUsec / float64(s.evalCalls)
}

// nestedPerOp is the mean server-side microseconds spent inside nested
// redis.call() commands per script call. Its time is part of serverPerOp.
func (s sample) nestedPerOp() float64 {
	if s.ops == 0 {
		return 0
	}
	var total float64
	for _, st := range s.nested {
		total += st.usec
	}
	return total / float64(s.ops)
}

// nestedCallsPerOp is the mean number of nested redis.call() commands.
func (s sample) nestedCallsPerOp() float64 {
	if s.ops == 0 {
		return 0
	}
	var total int64
	for _, st := range s.nested {
		total += st.calls
	}
	return float64(total) / float64(s.ops)
}

// luaPerOp is serverPerOp minus the nested command time: the Lua interpreter's
// own cost (closure creation, argument decoding, arithmetic, reply building)
// plus the EVALSHA dispatch itself.
func (s sample) luaPerOp() float64 { return s.serverPerOp() - s.nestedPerOp() }

// plan describes one measured block.
type plan struct {
	name string
	ops  int
	// scriptCmds names the commands that count as "the script call"
	// (evalsha, eval, fcall); everything else the server saw during a timed
	// chunk is treated as a nested redis.call().
	scriptCmds []string
	// chunk is how many ops run between two maintain() calls (0 = no chunking).
	chunk int
	// maintain restores the dataset between chunks. It runs outside the timed
	// window and outside the commandstats window, so its cost is not
	// attributed to the script. It must not use EVAL/EVALSHA/FCALL, which
	// would pollute the SLOWLOG filter.
	maintain func()
	body     func(i int)
}

// measure runs plan.body plan.ops times, timing each call, and attributes the
// server-side cost with commandstats deltas and SLOWLOG.
func measure(tb testing.TB, e *env, p plan) sample {
	tb.Helper()
	s := sample{name: p.name, ops: p.ops, wall: make([]time.Duration, 0, p.ops)}
	chunk := p.chunk
	if chunk <= 0 || chunk > p.ops {
		chunk = p.ops
	}

	if slowlogOn {
		if err := e.client.Do(e.ctx, e.client.B().SlowlogReset().Build()).Error(); err != nil {
			tb.Fatalf("slowlog reset: %v", err)
		}
	}
	delta := make(map[string]cmdStat, 32)
	for start := 0; start < p.ops; start += chunk {
		if p.maintain != nil {
			p.maintain()
		}
		end := min(start+chunk, p.ops)
		before := e.commandStats(tb)
		for i := start; i < end; i++ {
			t0 := time.Now()
			p.body(i)
			s.wall = append(s.wall, time.Since(t0))
		}
		after := e.commandStats(tb)
		for cmd, st := range diffStats(before, after) {
			cur := delta[cmd]
			cur.calls += st.calls
			cur.usec += st.usec
			delta[cmd] = cur
		}
	}

	script := make(map[string]bool, len(p.scriptCmds))
	for _, c := range p.scriptCmds {
		script[strings.ToLower(c)] = true
	}
	s.nested = make(map[string]cmdStat, len(delta))
	for cmd, st := range delta {
		if script[cmd] {
			s.evalCalls += st.calls
			s.evalUsec += st.usec
			continue
		}
		if cmd == "info" || cmd == "config|get" || cmd == "config|set" || cmd == "slowlog|reset" || cmd == "slowlog|get" {
			continue
		}
		s.nested[cmd] = st
	}

	if slowlogOn {
		ops := p.ops
		scriptCmds := p.scriptCmds
		entries := e.slowlogGet(tb, ops*4+256)
		want := make(map[string]bool, len(scriptCmds))
		for _, c := range scriptCmds {
			want[strings.ToUpper(c)] = true
		}
		var us []float64
		for _, en := range entries {
			if want[en.cmd] {
				us = append(us, float64(en.usec))
			}
		}
		sort.Float64s(us)
		s.slowN = len(us)
		if len(us) > 0 {
			s.slowP50 = us[len(us)*50/100]
			s.slowP99 = us[min(len(us)-1, len(us)*99/100)]
		}
	}
	return s
}

// pct returns the p-quantile of the wall clock samples.
func (s sample) pct(p float64) time.Duration {
	if len(s.wall) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), s.wall...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)) * p)
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

// report prints one sample as a row of the cost-attribution table and, when a
// *testing.B is given, publishes the same numbers as benchmark metrics.
func (s sample) report(tb testing.TB) {
	tb.Helper()
	line := fmt.Sprintf("%-38s ops=%-6d server=%7.1fus (lua=%6.1f nested=%6.1f over %4.1f cmds) slowlog p50=%6.1f p99=%7.1f | wall p50=%8s p99=%8s",
		s.name, s.ops, s.serverPerOp(), s.luaPerOp(), s.nestedPerOp(), s.nestedCallsPerOp(),
		s.slowP50, s.slowP99, s.pct(0.50).Round(time.Microsecond), s.pct(0.99).Round(time.Microsecond))
	tb.Log(line)
	fmt.Fprintln(os.Stderr, line)
	if b, ok := tb.(*testing.B); ok {
		b.ReportMetric(s.serverPerOp(), "us/server-op")
		b.ReportMetric(s.luaPerOp(), "us/lua-op")
		b.ReportMetric(s.nestedCallsPerOp(), "cmds/op")
	}
}

// reportNested prints the per-primitive breakdown of a sample.
func (s sample) reportNested(tb testing.TB) {
	tb.Helper()
	type row struct {
		cmd string
		st  cmdStat
	}
	rows := make([]row, 0, len(s.nested))
	for c, st := range s.nested {
		rows = append(rows, row{c, st})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].st.usec > rows[j].st.usec })
	for _, r := range rows {
		line := fmt.Sprintf("    %-30s %6.2f calls/op  %6.2f us/op  (%5.2f us/call)",
			r.cmd, float64(r.st.calls)/float64(s.ops), r.st.usec/float64(s.ops), r.st.usec/float64(r.st.calls))
		tb.Log(line)
		fmt.Fprintln(os.Stderr, line)
	}
}
