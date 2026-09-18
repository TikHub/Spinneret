package hotstate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/hotstate/hotstatedb"
	"github.com/TikHub/Spinneret/internal/policy"
	spsite "github.com/TikHub/Spinneret/internal/site"
)

// IdentityHot is the live Redis state of an identity. It mirrors the proto
// message IdentityHotState; time fields are the zero time when unset.
type IdentityHot struct {
	// Present reports whether the identity hash exists in Redis.
	Present bool
	// State is the lifecycle state as seen by the scheduler ("st").
	State string
	// ActiveLeases is the number of active leases ("al").
	ActiveLeases int
	// SiteCooldownUntil is the end of the identity × site cooldown ("scd").
	SiteCooldownUntil time.Time
	// SiteReuseUntil is the end of the site-level reuse interval ("sru").
	SiteReuseUntil time.Time
	// ExclusiveUntil is the end of the exclusive lease ("xl").
	ExclusiveUntil time.Time
	// BoundProxyID is the ID of the bound proxy, empty when unbound.
	BoundProxyID string
	// GlobalScore is the global health score with decay applied ("gs").
	GlobalScore float64
	// GlobalSamples is the number of observations behind GlobalScore ("gn").
	GlobalSamples int
	// AccountCooldownUntil is the end of the account cooldown ("acc" → "cd").
	AccountCooldownUntil time.Time
	// Groups holds the state per endpoint group of the identity's client,
	// ordered by endpoint group name.
	Groups []EndpointHot
}

// EndpointHot is the live state of an identity on one endpoint group.
type EndpointHot struct {
	EndpointGroupID string
	EndpointGroup   string
	Client          string
	// Score is the health score with decay applied (baseline when no entry exists).
	Score               float64
	Samples             int
	ConsecutiveFailures int
	CooldownUntil       time.Time
	ReuseUntil          time.Time
	LastUsedAt          time.Time
	// AvailableAt is the ready-queue score; zero when not in the queue.
	AvailableAt  time.Time
	InReadyQueue bool
}

// identityHotFields are the identity hash fields read by IdentityHotState.
var identityHotFields = []string{"st", "al", "scd", "sru", "xl", "px", "gs", "gts", "gn", "acc"}

// IdentityHotState reads the live Redis state of an identity of a site. It
// returns an apperr NotFound error when the identity does not exist in
// PostgreSQL; an identity missing from Redis yields Present=false with
// baseline scores and no queue membership.
func (s *Syncer) IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (IdentityHot, error) {
	var out IdentityHot
	if site == nil {
		return out, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "site is required")
	}
	ref, err := s.q.HotstateIdentityRef(ctx, hotstatedb.HotstateIdentityRefParams{ID: identityID, SiteID: site.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, apperr.NotFound("identity %s not found", identityID)
	}
	if err != nil {
		return out, fmt.Errorf("load identity %s: %w", identityID, err)
	}
	nowMs := s.now().UnixMilli()
	member := itoa(ref.Hkey)
	groups := newGroupPlan(site).groupsOfClient(ref.Client)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	cmds := make(rueidis.Commands, 0, 1+2*len(groups))
	cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.Identity(site.Key, ref.Hkey)).Field(identityHotFields...).Build())
	for _, g := range groups {
		cmds = append(cmds,
			s.rdb.B().Hget().Key(s.keys.Health(site.Key, g.Key)).Field(member).Build(),
			s.rdb.B().Zscore().Key(s.keys.Ready(site.Key, g.Key)).Member(member).Build())
	}
	results := s.doMulti(ctx, cmds)

	fields, err := results[0].ToArray()
	if err != nil {
		return out, fmt.Errorf("read identity hash: %w", err)
	}
	values := make(map[string]string, len(identityHotFields))
	for i, f := range identityHotFields {
		if i < len(fields) {
			if v, err := fields[i].ToString(); err == nil {
				values[f] = v
			}
		}
	}
	_, out.Present = values["st"]
	out.State = values["st"]
	out.ActiveLeases = int(parseInt(values["al"], 0))
	out.SiteCooldownUntil = msToTime(parseInt(values["scd"], 0))
	out.SiteReuseUntil = msToTime(parseInt(values["sru"], 0))
	out.ExclusiveUntil = msToTime(parseInt(values["xl"], 0))
	out.GlobalSamples = int(parseInt(values["gn"], 0))

	global := globalHealth(groups)
	out.GlobalScore = decayScore(parseFloat(values["gs"], global.Baseline), parseInt(values["gts"], nowMs),
		nowMs, global.Baseline, global.Tau.Std())

	out.Groups = make([]EndpointHot, 0, len(groups))
	for i, g := range groups {
		h := healthOf(g)
		eh := EndpointHot{EndpointGroupID: g.ID, EndpointGroup: g.Name, Client: g.Client, Score: h.Baseline}
		packed, err := results[1+2*i].ToString()
		switch {
		case err == nil:
			e := parseHealth(packed, h.Baseline, nowMs)
			eh.Score = decayScore(e.Score, e.ScoreTS, nowMs, h.Baseline, h.Tau.Std())
			eh.Samples = int(e.Samples)
			eh.ConsecutiveFailures = int(e.NFail)
			eh.CooldownUntil = msToTime(e.Cooldown)
			eh.ReuseUntil = msToTime(e.Reuse)
			eh.LastUsedAt = msToTime(e.LastUsed)
		case !rueidis.IsRedisNil(err):
			return out, fmt.Errorf("read health of group %s: %w", g.ID, err)
		}
		score, err := results[2+2*i].AsFloat64()
		switch {
		case err == nil:
			eh.InReadyQueue = true
			eh.AvailableAt = msToTime(int64(score))
		case !rueidis.IsRedisNil(err):
			return out, fmt.Errorf("read ready queue of group %s: %w", g.ID, err)
		}
		out.Groups = append(out.Groups, eh)
	}

	if err := s.readIdentityLinks(ctx, site, values, &out); err != nil {
		return out, err
	}
	return out, nil
}

// readIdentityLinks resolves the account cooldown and the bound proxy ID.
func (s *Syncer) readIdentityLinks(ctx context.Context, site *catalog.Site, values map[string]string, out *IdentityHot) error {
	var cmds rueidis.Commands
	accKey, accOK := parseKey(values["acc"])
	pxKey, pxOK := parseKey(values["px"])
	if accOK {
		cmds = append(cmds, s.rdb.B().Hget().Key(s.keys.Account(site.Key, accKey)).Field("cd").Build())
	}
	if pxOK {
		cmds = append(cmds, s.rdb.B().Hget().Key(s.keys.ProxySite(site.Key, pxKey)).Field("pid").Build())
	}
	if len(cmds) == 0 {
		return nil
	}
	results := s.doMulti(ctx, cmds)
	idx := 0
	if accOK {
		cd, err := results[idx].ToString()
		if err != nil && !rueidis.IsRedisNil(err) {
			return fmt.Errorf("read account cooldown: %w", err)
		}
		out.AccountCooldownUntil = msToTime(parseInt(cd, 0))
		idx++
	}
	if pxOK {
		pid, err := results[idx].ToString()
		if err != nil && !rueidis.IsRedisNil(err) {
			return fmt.Errorf("read bound proxy: %w", err)
		}
		if pid == "" {
			// Scoped to the site's namespace: a corrupt binding must never
			// reveal the ID of another tenant's proxy.
			rows, err := s.q.HotstateProxyIDsByHkeys(ctx, hotstatedb.HotstateProxyIDsByHkeysParams{
				NamespaceID: site.NamespaceID, Hkeys: []int64{pxKey},
			})
			if err != nil {
				return fmt.Errorf("resolve bound proxy: %w", err)
			}
			if len(rows) > 0 {
				pid = rows[0].ID
			}
		}
		out.BoundProxyID = pid
	}
	return nil
}

// ReadyCounts returns, per endpoint group hot-state key of the site, the
// number of identities available at now (ZCOUNT rdy -inf now).
func (s *Syncer) ReadyCounts(ctx context.Context, site *catalog.Site, now time.Time) (map[int64]int64, error) {
	if site == nil {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "site is required")
	}
	groups := newGroupPlan(site).groups
	out := make(map[int64]int64, len(groups))
	if len(groups) == 0 {
		return out, nil
	}
	maxScore := itoa(now.UnixMilli())
	cmds := make(rueidis.Commands, len(groups))
	for i, g := range groups {
		cmds[i] = s.rdb.B().Zcount().Key(s.keys.Ready(site.Key, g.Key)).Min("-inf").Max(maxScore).Build()
	}
	for i, res := range s.doMulti(ctx, cmds) {
		n, err := res.AsInt64()
		if err != nil {
			return nil, fmt.Errorf("count ready identities of group %s: %w", groups[i].ID, err)
		}
		out[groups[i].Key] = n
	}
	return out, nil
}

// healthOf returns the health parameters of a group's action policy, falling
// back to the built-in defaults.
func healthOf(g *catalog.EndpointGroup) policy.HealthSpec {
	if g != nil && g.Action != nil {
		return g.Action.Health
	}
	return defaultHealth()
}

// globalHealth picks the health parameters used to decay the global score:
// those of the client's "_default" group, else of the first group.
func globalHealth(groups []*catalog.EndpointGroup) policy.HealthSpec {
	for _, g := range groups {
		if g.Name == spsite.DefaultGroup {
			return healthOf(g)
		}
	}
	if len(groups) > 0 {
		return healthOf(groups[0])
	}
	return defaultHealth()
}

func defaultHealth() policy.HealthSpec {
	if spec, ok := policy.Default(policy.KindAction).(*policy.ActionSpec); ok && spec != nil {
		return spec.Health
	}
	return policy.HealthSpec{Baseline: policy.DefaultHealthBaseline}
}

// parseKey parses a non-empty hot-state key reference.
func parseKey(v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
