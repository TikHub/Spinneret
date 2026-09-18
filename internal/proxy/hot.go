package proxy

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Health score parameters of the proxy x site EWMA (spec §6.4).
const (
	HealthAlpha        = 0.1
	HealthBaseline     = 70.0
	HealthTau          = 6 * time.Hour
	healthResetAfter   = time.Hour
	observationSuccess = 100
	observationFailure = 0
)

// redisCommandsPerRun bounds the commands built and sent in one pipeline.
const redisCommandsPerRun = 1000

var (
	//go:embed lua/cooldown.lua
	cooldownLua string
	//go:embed lua/reset.lua
	resetLua string
	//go:embed lua/health.lua
	healthLua string

	cooldownScript = redis.NewScript("proxy_cooldown", cooldownLua)
	resetScript    = redis.NewScript("proxy_reset", resetLua)
	healthScript   = redis.NewScript("proxy_health", healthLua)
)

// Proxy hash fields (spec §5.4).
const (
	fieldCooldown       = "cd"
	fieldGlobalCooldown = "gcd"
)

// execChunked runs n script calls, built on demand by call(i), in pipelines of
// at most redisCommandsPerRun calls and returns the first error.
func execChunked(ctx context.Context, rdb rueidis.Client, script *redis.Script, n int, call func(i int) rueidis.LuaExec) error {
	batch := make([]rueidis.LuaExec, 0, min(n, redisCommandsPerRun))
	for start := 0; start < n; start += redisCommandsPerRun {
		batch = batch[:0]
		for i := start; i < min(start+redisCommandsPerRun, n); i++ {
			batch = append(batch, call(i))
		}
		for _, res := range script.ExecMulti(ctx, rdb, batch...) {
			if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
				return fmt.Errorf("run %s script: %w", script.Name, err)
			}
		}
	}
	return nil
}

// sortedSites returns the sites of ns ordered by name.
func sortedSites(ns *catalog.Namespace) []*catalog.Site {
	if ns == nil {
		return nil
	}
	sites := make([]*catalog.Site, 0, len(ns.SitesByID))
	for _, s := range ns.SitesByID {
		sites = append(sites, s)
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	return sites
}

// visibleSites returns the sites of ns whose proxy state p may read.
func visibleSites(p *authz.Principal, ns *catalog.Namespace) []*catalog.Site {
	all := sortedSites(ns)
	out := all[:0:0]
	for _, s := range all {
		if p.Can(authz.PermProxyRead, siteResource(ns, s)) {
			out = append(out, s)
		}
	}
	return out
}

// siteStateFields are read from "px:<p>" for the console view.
var siteStateFields = []string{"pid", "st", "sc", "sts", "sn", "al", "cd"}

// readSiteStates fills Proxy.Sites from the hot state of sites.
func (s *Service) readSiteStates(ctx context.Context, sites []*catalog.Site, views []*Proxy) error {
	for _, v := range views {
		v.Sites = []SiteState{}
	}
	if s.rdb == nil || len(sites) == 0 {
		return nil
	}
	now := s.now()
	total := len(views) * len(sites)
	cmds := make(rueidis.Commands, 0, min(total, redisCommandsPerRun))
	for start := 0; start < total; start += redisCommandsPerRun {
		end := min(start+redisCommandsPerRun, total)
		cmds = cmds[:0]
		for i := start; i < end; i++ {
			v, site := views[i/len(sites)], sites[i%len(sites)]
			cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.ProxySite(site.Key, v.Key)).Field(siteStateFields...).Build())
		}
		for j, res := range s.rdb.DoMulti(ctx, cmds...) {
			vals, err := res.ToArray()
			if err != nil {
				return fmt.Errorf("read proxy hot state: %w", err)
			}
			st, ok := decodeSiteState(vals, now)
			if !ok {
				continue
			}
			i := start + j
			v, site := views[i/len(sites)], sites[i%len(sites)]
			st.SiteID, st.Site = site.ID, site.Name
			v.Sites = append(v.Sites, st)
		}
	}
	return nil
}

// decodeSiteState decodes HMGET siteStateFields; ok is false when the hash is missing.
func decodeSiteState(vals []rueidis.RedisMessage, now time.Time) (SiteState, bool) {
	str := func(i int) (string, bool) {
		if i >= len(vals) || vals[i].IsNil() {
			return "", false
		}
		v, err := vals[i].ToString()
		return v, err == nil
	}
	_, hasPid := str(0)
	state, hasState := str(1)
	if !hasPid && !hasState {
		return SiteState{}, false
	}
	out := SiteState{State: state, Score: HealthBaseline}
	if sc, ok := str(2); ok {
		score := parseFloat(sc, HealthBaseline)
		sts, _ := str(3)
		out.Score = DecayScore(score, parseInt(sts, now.UnixMilli()), now.UnixMilli())
	}
	sn, _ := str(4)
	out.Samples = int(parseInt(sn, 0))
	al, _ := str(5)
	out.ActiveLeases = int(parseInt(al, 0))
	cd, _ := str(6)
	if ms := parseInt(cd, 0); ms > 0 {
		t := time.UnixMilli(ms).UTC()
		out.CooldownUntil = &t
	}
	return out, true
}

// DecayScore regresses score towards HealthBaseline for the time elapsed
// between stsMs and nowMs (tau HealthTau), rounded to two decimals.
func DecayScore(score float64, stsMs, nowMs int64) float64 {
	dt := nowMs - stsMs
	if dt > 0 {
		score = HealthBaseline + (score-HealthBaseline)*math.Exp(-float64(dt)/float64(HealthTau.Milliseconds()))
	}
	return math.Round(score*100) / 100
}

func parseInt(s string, def int64) int64 {
	if s == "" {
		return def
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return int64(f)
	}
	return def
}

func parseFloat(s string, def float64) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return f
}

// writeCooldown sets cd (site) or gcd (global) of the given proxies on sites.
func writeCooldown(ctx context.Context, rdb rueidis.Client, keys redis.Keys, sites []*catalog.Site, field string, until time.Time, proxies []hotRef) error {
	if rdb == nil {
		return nil
	}
	ms := strconv.FormatInt(until.UnixMilli(), 10)
	return execChunked(ctx, rdb, cooldownScript, len(sites)*len(proxies), func(i int) rueidis.LuaExec {
		site, p := sites[i/len(proxies)], proxies[i%len(proxies)]
		return rueidis.LuaExec{
			Keys: []string{keys.SiteMeta(site.Key)},
			Args: []string{strconv.FormatInt(p.key, 10), field, ms, p.id},
		}
	})
}

// resetStats clears the health statistics of proxies on sites.
func resetStats(ctx context.Context, rdb rueidis.Client, keys redis.Keys, sites []*catalog.Site, proxies []hotRef) error {
	if rdb == nil {
		return nil
	}
	return execChunked(ctx, rdb, resetScript, len(sites)*len(proxies), func(i int) rueidis.LuaExec {
		site, p := sites[i/len(proxies)], proxies[i%len(proxies)]
		return rueidis.LuaExec{Keys: []string{keys.SiteMeta(site.Key)}, Args: []string{strconv.FormatInt(p.key, 10)}}
	})
}

// recordHealth applies one health observation of a proxy on every site.
func recordHealth(ctx context.Context, rdb rueidis.Client, keys redis.Keys, sites []*catalog.Site, proxyKey int64, ok bool, now time.Time) error {
	if rdb == nil || len(sites) == 0 {
		return nil
	}
	obs, flag := strconv.Itoa(observationFailure), "0"
	if ok {
		obs, flag = strconv.Itoa(observationSuccess), "1"
	}
	args := []string{
		strconv.FormatInt(proxyKey, 10), obs, strconv.FormatInt(now.UnixMilli(), 10),
		strconv.FormatFloat(HealthAlpha, 'f', -1, 64), strconv.FormatFloat(HealthBaseline, 'f', -1, 64),
		strconv.FormatInt(HealthTau.Milliseconds(), 10), strconv.FormatInt(healthResetAfter.Milliseconds(), 10), flag,
	}
	return execChunked(ctx, rdb, healthScript, len(sites), func(i int) rueidis.LuaExec {
		return rueidis.LuaExec{Keys: []string{keys.SiteMeta(sites[i].Key)}, Args: args}
	})
}

// hotRef identifies a proxy in the hot state.
type hotRef struct {
	id  string
	key int64
}

// StateEventData is the data of proxy.state events (see the package documentation).
type StateEventData struct {
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	SiteID      string     `json:"site_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Action      string     `json:"action"`
	Until       *time.Time `json:"until"`
	Reason      string     `json:"reason"`
}

// publishState publishes proxy.state events; failures are logged by the caller.
func publishState(ctx context.Context, bus events.Bus, ns *catalog.Namespace, items []StateEventData) error {
	if bus == nil || ns == nil {
		return nil
	}
	var firstErr error
	for _, it := range items {
		it.SubjectKind = auditResourceKind
		data, err := json.Marshal(it)
		if err != nil {
			return fmt.Errorf("encode proxy event: %w", err)
		}
		ev := events.Event{
			Type: events.TypeProxyState, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: it.SiteID, Data: data,
		}
		if err := bus.Publish(ctx, events.NamespaceChannel(ns.ID), ev); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("publish proxy event: %w", err)
		}
	}
	return firstErr
}
