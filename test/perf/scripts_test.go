//go:build perf

package perf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/redis/rueidis"

	storeredis "github.com/TikHub/Spinneret/internal/store/redis"
)

// repoRoot is the module root, derived from this file's compile-time path so
// the harness can read the production .lua sources without importing the
// unexported embed variables of their owning packages.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("perf: cannot locate repo root")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// luaFile reads one script source relative to the repo root.
func luaFile(tb testing.TB, rel string) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), rel))
	if err != nil {
		tb.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// joinLua mirrors internal/scheduler.joinLua.
func joinLua(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.String()
}

// scriptSet holds the production scripts, assembled exactly as their owning
// packages assemble them, plus the calibration scripts of the harness.
type scriptSet struct {
	acquire     *storeredis.Script
	release     *storeredis.Script
	reap        *storeredis.Script
	renew       *storeredis.Script
	observe     *storeredis.Script
	apply       *storeredis.Script
	ingest      *storeredis.Script
	leaseRetain *storeredis.Script
	breakerEval *storeredis.Script

	// empty is "return 0" with no prelude: pure EVALSHA dispatch.
	empty *rueidis.Lua
	// prelude is common.lua followed by "return 0": dispatch plus the cost of
	// creating the 17 chunk-level helper closures on every call.
	prelude *storeredis.Script
	// acquireSrc is the assembled acquire source, reused by the FUNCTION
	// prototype.
	acquireSrc string
	commonSrc  string
	// acquireParts are the six sources acquire is assembled from, in order:
	// acquire (arguments and shared state), filters, proxy, write, select,
	// main (control flow). BenchmarkAcquirePhases truncates the assembly after
	// each part to attribute the interpreter cost.
	acquireParts []string
}

var (
	scriptsOnce sync.Once
	scriptsVal  *scriptSet
)

// scripts assembles the production scripts once per process.
func scripts(tb testing.TB) *scriptSet {
	tb.Helper()
	scriptsOnce.Do(func() {
		s := &scriptSet{}
		s.commonSrc = luaFile(tb, "internal/store/redis/lua/common.lua")
		leaseEnd := luaFile(tb, "internal/scheduler/lua/lease_end.lua")
		s.acquireParts = []string{
			luaFile(tb, "internal/scheduler/lua/acquire.lua"),
			luaFile(tb, "internal/scheduler/lua/acquire_filters.lua"),
			luaFile(tb, "internal/scheduler/lua/acquire_proxy.lua"),
			luaFile(tb, "internal/scheduler/lua/acquire_write.lua"),
			luaFile(tb, "internal/scheduler/lua/acquire_select.lua"),
			luaFile(tb, "internal/scheduler/lua/acquire_main.lua"),
		}
		s.acquireSrc = joinLua(s.acquireParts...)
		s.acquire = storeredis.NewScript("acquire", s.acquireSrc)
		s.release = storeredis.NewScript("release", joinLua(leaseEnd, luaFile(tb, "internal/scheduler/lua/release.lua")))
		s.reap = storeredis.NewScript("reap", joinLua(leaseEnd, luaFile(tb, "internal/scheduler/lua/reap.lua")))
		s.renew = storeredis.NewScript("renew", luaFile(tb, "internal/scheduler/lua/renew.lua"))
		s.observe = storeredis.NewScript("observe", luaFile(tb, "internal/worker/lua/observe.lua"))
		s.apply = storeredis.NewScript("apply", luaFile(tb, "internal/action/lua/apply.lua"))
		s.ingest = storeredis.NewScript("ingest", luaFile(tb, "internal/signal/lua/ingest.lua"))
		s.leaseRetain = storeredis.NewScript("lease_retain", luaFile(tb, "internal/signal/lua/lease_retain.lua"))
		s.breakerEval = storeredis.NewScript("breaker_eval", luaFile(tb, "internal/breaker/lua/breaker_eval.lua"))
		s.empty = rueidis.NewLuaScript("return 0")
		s.prelude = storeredis.NewScript("prelude_only", "return 0")
		scriptsVal = s
	})
	return scriptsVal
}

// runScript runs a script and fails the benchmark on error.
func runScript(tb testing.TB, e *env, s *storeredis.Script, keys, args []string) rueidis.RedisResult {
	res := s.Exec(e.ctx, e.client, keys, args)
	if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
		tb.Fatalf("%s: %v", s.Name, err)
	}
	return res
}

// warm makes sure the script is in the server cache so the first measured call
// is an EVALSHA hit and not an EVAL that compiles the source.
func warm(tb testing.TB, e *env, s *storeredis.Script) {
	tb.Helper()
	if err := e.client.Do(e.ctx, e.client.B().ScriptLoad().Script(s.Source()).Build()).Error(); err != nil {
		tb.Fatalf("script load %s: %v", s.Name, err)
	}
}
