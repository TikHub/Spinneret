package action

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

//go:embed lua/apply.lua
var applyLua string

// apply.lua operation codes.
const (
	luaCooldown   = "cd"
	luaBan        = "ban"
	luaExpire     = "exp"
	luaQuarantine = "qua"
	luaActivate   = "act"
	luaSet        = "set"
	luaClear      = "clr"
	luaResetStats = "rst"
)

// apply.lua scope codes.
const (
	luaScopeIdentityEndpoint = "ie"
	luaScopeIdentitySite     = "is"
	luaScopeAccount          = "ac"
	luaScopeProxySite        = "ps"
	luaScopeProxyGlobal      = "pg"
	luaScopeIdentity         = "id"
	luaScopeProxy            = "px"
)

// apply.lua flags.
const (
	flagAutomatic    = 1
	flagForce        = 2
	flagResetFails   = 4
	flagResetHealth  = 8
	flagRecordBans   = 16
	luaPermanent     = -1
	maxOpsPerCall    = 256
	redisCallTimeout = 5 * time.Second
)

// Skip reasons returned by apply.lua.
const (
	skipMissing        = "missing"
	skipAlreadyCooling = "already_cooling"
)

// luaOp is one apply.lua operation (JSON encoded into ARGV).
type luaOp struct {
	Op        string             `json:"op"`
	Scope     string             `json:"sc,omitempty"`
	Subject   int64              `json:"s"`
	Group     int64              `json:"eg,omitempty"`
	Until     int64              `json:"u,omitempty"`
	Flags     int                `json:"f,omitempty"`
	Groups    []int64            `json:"egs,omitempty"`
	Add       []int64            `json:"add,omitempty"`
	To        string             `json:"to,omitempty"`
	Trim      int64              `json:"trim,omitempty"`
	PrevFail  int                `json:"pn,omitempty"`
	Baseline  float64            `json:"bl,omitempty"`
	Baselines map[string]float64 `json:"bls,omitempty"`
	Counters  []string           `json:"cnt,omitempty"`
	Global    int                `json:"gl,omitempty"`
}

// memberChange is a member identity changed by an account operation.
type memberChange struct {
	Key      int64
	ID       string
	From, To string
}

// luaResult is the apply.lua result of one operation.
type luaResult struct {
	Applied  bool
	Reason   string
	From, To string
	Until    int64
	Members  []memberChange
}

// siteCall is one apply.lua invocation for a site.
type siteCall struct {
	SiteKey int64
	Ops     []luaOp
	// Token is the idempotency token prefix of the call ("" = none). The
	// applier appends a digest of the call's arguments (now and operations),
	// and apply.lua records the results of the first call with that token and
	// returns them to every identical retry instead of applying the
	// operations again.
	Token string
}

// applier runs apply.lua.
type applier struct {
	rdb    rueidis.Client
	keys   redis.Keys
	script *redis.Script
}

func newApplier(rdb rueidis.Client, keys redis.Keys) *applier {
	return &applier{rdb: rdb, keys: keys, script: redis.NewScript("action.apply", applyLua)}
}

// run applies ops to one site, splitting them into calls of at most
// maxOpsPerCall operations. Results are returned in op order.
func (a *applier) run(ctx context.Context, siteKey int64, now time.Time, ops []luaOp) ([]luaResult, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	calls := make([]siteCall, 0, (len(ops)+maxOpsPerCall-1)/maxOpsPerCall)
	for start := 0; start < len(ops); start += maxOpsPerCall {
		end := min(start+maxOpsPerCall, len(ops))
		calls = append(calls, siteCall{SiteKey: siteKey, Ops: ops[start:end]})
	}
	results, errs := a.runMulti(ctx, now, calls)
	out := make([]luaResult, 0, len(ops))
	for i := range calls {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, results[i]...)
	}
	return out, nil
}

// runMulti executes several site calls in one pipeline. Each call gets its
// own result slice and error.
func (a *applier) runMulti(ctx context.Context, now time.Time, calls []siteCall) ([][]luaResult, []error) {
	results := make([][]luaResult, len(calls))
	errs := make([]error, len(calls))
	if len(calls) == 0 {
		return results, errs
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, redisCallTimeout)
		defer cancel()
	}
	execs := make([]rueidis.LuaExec, 0, len(calls))
	index := make([]int, 0, len(calls))
	nowMs := strconv.FormatInt(now.UnixMilli(), 10)
	for i, c := range calls {
		args, err := encodeOps(nowMs, c.Token, c.Ops)
		if err != nil {
			errs[i] = err
			continue
		}
		execs = append(execs, rueidis.LuaExec{Keys: []string{a.keys.SiteMeta(c.SiteKey)}, Args: args})
		index = append(index, i)
	}
	if len(execs) == 0 {
		return results, errs
	}
	for j, r := range a.script.ExecMulti(ctx, a.rdb, execs...) {
		i := index[j]
		res, err := parseApplyResults(r, len(calls[i].Ops))
		if err != nil {
			errs[i] = fmt.Errorf("apply.lua on site %d: %w", calls[i].SiteKey, err)
			continue
		}
		results[i] = res
	}
	return results, errs
}

// encodeOps builds the ARGV of one apply.lua call. A non-empty token prefix
// is completed with a SHA-256 digest of now and the encoded operations.
func encodeOps(nowMs, token string, ops []luaOp) ([]string, error) {
	args := make([]string, 2, len(ops)+2)
	args[0] = nowMs
	for _, op := range ops {
		b, err := json.Marshal(op)
		if err != nil {
			return nil, fmt.Errorf("encode apply operation %s: %w", op.Op, err)
		}
		args = append(args, string(b))
	}
	if token != "" {
		h := sha256.New()
		for _, a := range append([]string{nowMs}, args[2:]...) {
			h.Write([]byte(a))
			h.Write([]byte{0})
		}
		args[1] = token + ":" + hex.EncodeToString(h.Sum(nil)[:12])
	}
	return args, nil
}

func parseApplyResults(r rueidis.RedisResult, want int) ([]luaResult, error) {
	arr, err := r.ToArray()
	if err != nil {
		return nil, err
	}
	if len(arr) != want {
		return nil, fmt.Errorf("got %d results, want %d", len(arr), want)
	}
	out := make([]luaResult, len(arr))
	for i, item := range arr {
		fields, err := item.ToArray()
		if err != nil {
			return nil, fmt.Errorf("result %d: %w", i, err)
		}
		if len(fields) != 6 {
			return nil, fmt.Errorf("result %d: got %d fields, want 6", i, len(fields))
		}
		applied, err := fields[0].AsInt64()
		if err != nil {
			return nil, fmt.Errorf("result %d applied flag: %w", i, err)
		}
		res := luaResult{Applied: applied == 1}
		if res.Reason, err = fields[1].ToString(); err != nil {
			return nil, fmt.Errorf("result %d reason: %w", i, err)
		}
		if res.From, err = fields[2].ToString(); err != nil {
			return nil, fmt.Errorf("result %d from: %w", i, err)
		}
		if res.To, err = fields[3].ToString(); err != nil {
			return nil, fmt.Errorf("result %d to: %w", i, err)
		}
		until, err := fields[4].ToString()
		if err != nil {
			return nil, fmt.Errorf("result %d until: %w", i, err)
		}
		if res.Until, err = strconv.ParseInt(until, 10, 64); err != nil {
			return nil, fmt.Errorf("result %d until %q: %w", i, until, err)
		}
		if res.Members, err = parseMembers(fields[5]); err != nil {
			return nil, fmt.Errorf("result %d members: %w", i, err)
		}
		out[i] = res
	}
	return out, nil
}

func parseMembers(m rueidis.RedisMessage) ([]memberChange, error) {
	items, err := m.AsStrSlice()
	if err != nil {
		if rueidis.IsRedisNil(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]memberChange, 0, len(items))
	for _, s := range items {
		parts := strings.Split(s, "|")
		if len(parts) != 4 {
			return nil, fmt.Errorf("malformed member %q", s)
		}
		key, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed member key %q: %w", s, err)
		}
		out = append(out, memberChange{Key: key, ID: parts[1], From: parts[2], To: parts[3]})
	}
	return out, nil
}

// untilMs converts an absolute end time into apply.lua's encoding.
func untilMs(t time.Time, permanent bool) int64 {
	if permanent {
		return luaPermanent
	}
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// msTime converts a Unix millisecond value (>0) into a time.
func msTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// groupKeys returns the hkeys of the endpoint groups of client ("" = every
// group of the site) sorted ascending for deterministic scripts.
func groupKeys(s *catalog.Site, client string) []int64 {
	if s == nil {
		return nil
	}
	out := make([]int64, 0, len(s.GroupsByID))
	for _, g := range s.GroupsByID {
		if client == "" || g.Client == client {
			out = append(out, g.Key)
		}
	}
	slices.Sort(out)
	return out
}

// eligibleGroupKeys returns the groups of client whose rotation admits typeID.
func eligibleGroupKeys(s *catalog.Site, client, typeID string) []int64 {
	if s == nil {
		return nil
	}
	out := make([]int64, 0, len(s.GroupsByID))
	for _, g := range s.GroupsByID {
		if g.Client != client {
			continue
		}
		for _, id := range g.IdentityTypeIDs {
			if id == typeID {
				out = append(out, g.Key)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// baselines returns the health baseline of each group of client.
func baselines(s *catalog.Site, client string) map[string]float64 {
	if s == nil {
		return nil
	}
	out := make(map[string]float64)
	for _, g := range s.GroupsByID {
		if (client == "" || g.Client == client) && g.Action != nil {
			out[strconv.FormatInt(g.Key, 10)] = g.Action.Health.Baseline
		}
	}
	return out
}

// counterSuffixes returns the identity counter key suffixes
// ("cnt:i<hkey>:<outcome>:<windowMs>") referenced by the action policies of
// the groups of client.
func counterSuffixes(s *catalog.Site, client string, identityKey int64) []string {
	if s == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	subject := "i" + strconv.FormatInt(identityKey, 10)
	for _, g := range s.GroupsByID {
		if g.Client != client || g.Action == nil {
			continue
		}
		for _, outcome := range policy.Outcomes() {
			for _, req := range g.Action.CounterRequests(outcome) {
				if req.Subject == policy.SubjectProxy {
					continue
				}
				suffix := "cnt:" + subject + ":" + req.Outcome + ":" + strconv.FormatInt(req.Window.Milliseconds(), 10)
				if _, ok := seen[suffix]; ok {
					continue
				}
				seen[suffix] = struct{}{}
				out = append(out, suffix)
			}
		}
	}
	return out
}
