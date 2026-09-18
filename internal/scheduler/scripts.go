package scheduler

import (
	_ "embed"

	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Lua sources. acquire.lua is split by concern; the parts are concatenated
// in declaration order into one script body. release.lua and reap.lua share
// lease_end.lua.
var (
	//go:embed lua/acquire.lua
	acquireLua string
	//go:embed lua/acquire_filters.lua
	acquireFiltersLua string
	//go:embed lua/acquire_proxy.lua
	acquireProxyLua string
	//go:embed lua/acquire_write.lua
	acquireWriteLua string
	//go:embed lua/acquire_select.lua
	acquireSelectLua string
	//go:embed lua/acquire_main.lua
	acquireMainLua string
	//go:embed lua/renew.lua
	renewLua string
	//go:embed lua/lease_end.lua
	leaseEndLua string
	//go:embed lua/release.lua
	releaseLua string
	//go:embed lua/reap.lua
	reapLua string
)

// Scripts are immutable and safe for concurrent use; they are package-level
// values like other embedded files.
var (
	acquireScript = redis.NewScript("acquire", joinLua(acquireLua, acquireFiltersLua, acquireProxyLua,
		acquireWriteLua, acquireSelectLua, acquireMainLua))
	renewScript   = redis.NewScript("renew", renewLua)
	releaseScript = redis.NewScript("release", joinLua(leaseEndLua, releaseLua))
	reapScript    = redis.NewScript("reap", joinLua(leaseEndLua, reapLua))
)

// joinLua concatenates script parts, each on its own lines.
func joinLua(parts ...string) string {
	n := 0
	for _, p := range parts {
		n += len(p) + 1
	}
	b := make([]byte, 0, n)
	for _, p := range parts {
		b = append(b, p...)
		b = append(b, '\n')
	}
	return string(b)
}
