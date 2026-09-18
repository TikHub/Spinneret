package policy

import (
	"time"

	"github.com/TikHub/Spinneret/internal/pkg/durationx"
)

// Rotation strategies.
const (
	StrategyWeightedRandom    = "weighted_random"
	StrategyLeastRecentlyUsed = "least_recently_used"
	StrategyRoundRobin        = "round_robin"
	StrategyBestHealth        = "best_health"
)

// Reuse anchors and scopes.
const (
	ReuseAnchorAcquired     = "acquired"
	ReuseAnchorReleased     = "released"
	ReuseScopeEndpointGroup = "endpoint_group"
	ReuseScopeSite          = "site"
)

// Proxy assignment modes and proxy kinds.
const (
	ProxyModeNone         = "none"
	ProxyModePool         = "pool"
	ProxyModeBindIdentity = "bind_identity"
	ProxyModeRegionMatch  = "region_match"

	ProxyKindDatacenter  = "datacenter"
	ProxyKindResidential = "residential"
	ProxyKindMobile      = "mobile"
	ProxyKindTunnel      = "tunnel"
)

// Rotation defaults and limits (spec §7).
const (
	DefaultCandidateSample     = 32
	DefaultLeaseTTL            = 120 * time.Second
	DefaultMaxLeaseLifetime    = 30 * time.Minute
	DefaultMaxConcurrentLeases = 1
	DefaultStickyTTL           = 10 * time.Minute
	DefaultWarmupQuotaFactor   = 1.0
	DefaultProbeWeightFactor   = 0.1
	DefaultProbeMaxLeases      = 2
	DefaultRebindTolerance     = 5 * time.Minute
	DefaultMaxRebindsPerDay    = 3

	maxCandidateSample     = 256
	minLeaseTTL            = 5 * time.Second
	maxLeaseTTL            = 30 * time.Minute
	maxLeaseLifetime       = 24 * time.Hour
	maxConcurrentLeases    = 10000
	maxIdentityTypeNameLen = 64
)

// RotationSpec is a rotation policy: which identity types are eligible and how
// identities and proxies are selected for an endpoint group.
type RotationSpec struct {
	Name          string         `yaml:"name" json:"name"`
	Description   string         `yaml:"description,omitempty" json:"description,omitempty"`
	Bind          *Binding       `yaml:"bind,omitempty" json:"bind,omitempty"`
	IdentityTypes StringList     `yaml:"identity_types" json:"identity_types"`
	Rotation      RotationParams `yaml:"rotation" json:"rotation"`
	Proxy         ProxySpec      `yaml:"proxy" json:"proxy"`
}

// RotationParams holds the identity selection parameters.
type RotationParams struct {
	Strategy            string             `yaml:"strategy" json:"strategy"`
	CandidateSample     int                `yaml:"candidate_sample" json:"candidate_sample"`
	LeaseTTL            durationx.Duration `yaml:"lease_ttl" json:"lease_ttl"`
	MaxLeaseLifetime    durationx.Duration `yaml:"max_lease_lifetime" json:"max_lease_lifetime"`
	MaxConcurrentLeases int                `yaml:"max_concurrent_leases" json:"max_concurrent_leases"`
	ReuseInterval       durationx.Duration `yaml:"reuse_interval" json:"reuse_interval"`
	ReuseAnchor         string             `yaml:"reuse_anchor" json:"reuse_anchor"`
	ReuseScope          string             `yaml:"reuse_scope" json:"reuse_scope"`
	Quota               []QuotaSpec        `yaml:"quota" json:"quota"`
	Sticky              StickySpec         `yaml:"sticky" json:"sticky"`
	Warmup              WarmupSpec         `yaml:"warmup" json:"warmup"`
	Probe               ProbeSpec          `yaml:"probe" json:"probe"`
}

// QuotaSpec limits requests per identity per endpoint group within a window.
type QuotaSpec struct {
	Limit  int64              `yaml:"limit" json:"limit"`
	Window durationx.Duration `yaml:"window" json:"window"`
}

// StickySpec configures session-key stickiness.
type StickySpec struct {
	Enabled bool               `yaml:"enabled" json:"enabled"`
	TTL     durationx.Duration `yaml:"ttl" json:"ttl"`
}

// WarmupSpec scales quotas of freshly activated identities.
type WarmupSpec struct {
	Duration    durationx.Duration `yaml:"duration" json:"duration"`
	QuotaFactor float64            `yaml:"quota_factor" json:"quota_factor"`
}

// ProbeSpec configures traffic to pending identities.
type ProbeSpec struct {
	WeightFactor float64 `yaml:"weight_factor" json:"weight_factor"`
	MaxLeases    int     `yaml:"max_leases" json:"max_leases"`
}

// ProxySpec configures proxy assignment.
type ProxySpec struct {
	Mode             string             `yaml:"mode" json:"mode"`
	Kinds            StringList         `yaml:"kinds" json:"kinds"`
	Tags             StringList         `yaml:"tags" json:"tags"`
	Providers        StringList         `yaml:"providers" json:"providers"`
	Regions          StringList         `yaml:"regions" json:"regions"`
	RegionMatch      bool               `yaml:"region_match" json:"region_match"`
	RebindTolerance  durationx.Duration `yaml:"rebind_tolerance" json:"rebind_tolerance"`
	MaxRebindsPerDay int                `yaml:"max_rebinds_per_day" json:"max_rebinds_per_day"`
}

// newRotationBase returns an unnamed rotation spec carrying every default.
func newRotationBase() *RotationSpec {
	s := &RotationSpec{
		Proxy: ProxySpec{
			RebindTolerance:  durationx.Duration(DefaultRebindTolerance),
			MaxRebindsPerDay: DefaultMaxRebindsPerDay,
		},
	}
	s.ApplyDefaults()
	return s
}

// Kind implements Spec.
func (s *RotationSpec) Kind() Kind { return KindRotation }

// PolicyName implements Spec.
func (s *RotationSpec) PolicyName() string {
	if s == nil {
		return ""
	}
	return s.Name
}

// PolicyBinding implements Spec.
func (s *RotationSpec) PolicyBinding() *Binding {
	if s == nil {
		return nil
	}
	return copyBinding(s.Bind)
}

// ApplyDefaults fills zero values whose zero is not a legal setting. The
// proxy rebind tolerance and daily rebind limit accept 0 and are therefore
// only defaulted by the parser.
func (s *RotationSpec) ApplyDefaults() {
	if s == nil {
		return
	}
	s.IdentityTypes = nilIfEmpty(s.IdentityTypes)
	r := &s.Rotation
	setString(&r.Strategy, StrategyWeightedRandom)
	setInt(&r.CandidateSample, DefaultCandidateSample)
	setDuration(&r.LeaseTTL, DefaultLeaseTTL)
	setDuration(&r.MaxLeaseLifetime, DefaultMaxLeaseLifetime)
	setInt(&r.MaxConcurrentLeases, DefaultMaxConcurrentLeases)
	setString(&r.ReuseAnchor, ReuseAnchorReleased)
	setString(&r.ReuseScope, ReuseScopeEndpointGroup)
	if r.Quota == nil {
		r.Quota = []QuotaSpec{}
	}
	setDuration(&r.Sticky.TTL, DefaultStickyTTL)
	if r.Warmup.QuotaFactor == 0 {
		r.Warmup.QuotaFactor = DefaultWarmupQuotaFactor
	}
	if r.Probe.WeightFactor == 0 {
		r.Probe.WeightFactor = DefaultProbeWeightFactor
	}
	setInt(&r.Probe.MaxLeases, DefaultProbeMaxLeases)
	p := &s.Proxy
	setString(&p.Mode, ProxyModeNone)
	p.Kinds = nilIfEmpty(p.Kinds)
	p.Tags = nilIfEmpty(p.Tags)
	p.Providers = nilIfEmpty(p.Providers)
	p.Regions = nilIfEmpty(p.Regions)
}

// Validate implements Spec.
func (s *RotationSpec) Validate() error {
	if s == nil {
		return &ValidationError{Kind: KindRotation, Problems: []Problem{{Message: "spec is nil"}}}
	}
	v := &validator{}
	validateCommon(v, s.Name, s.Description, s.Bind)
	v.stringList("identity_types", s.IdentityTypes, func(path, t string) {
		if len(t) > maxIdentityTypeNameLen {
			v.addf(path, "must be at most %d characters", maxIdentityTypeNameLen)
		}
	})
	s.Rotation.validate(v, "rotation")
	s.Proxy.validate(v, "proxy")
	return v.result(KindRotation, s.Name)
}

func (r *RotationParams) validate(v *validator, path string) {
	v.enum(field(path, "strategy"), r.Strategy,
		StrategyWeightedRandom, StrategyLeastRecentlyUsed, StrategyRoundRobin, StrategyBestHealth)
	v.intRange(field(path, "candidate_sample"), int64(r.CandidateSample), 1, maxCandidateSample)
	v.duration(field(path, "lease_ttl"), r.LeaseTTL, minLeaseTTL, maxLeaseTTL)
	v.duration(field(path, "max_lease_lifetime"), r.MaxLeaseLifetime, 0, maxLeaseLifetime)
	if !r.MaxLeaseLifetime.IsPermanent() && !r.LeaseTTL.IsPermanent() && r.MaxLeaseLifetime < r.LeaseTTL {
		v.addf(field(path, "max_lease_lifetime"), "must be at least lease_ttl (%s)", r.LeaseTTL)
	}
	v.intRange(field(path, "max_concurrent_leases"), int64(r.MaxConcurrentLeases), 1, maxConcurrentLeases)
	v.duration(field(path, "reuse_interval"), r.ReuseInterval, 0, 0)
	v.enum(field(path, "reuse_anchor"), r.ReuseAnchor, ReuseAnchorAcquired, ReuseAnchorReleased)
	v.enum(field(path, "reuse_scope"), r.ReuseScope, ReuseScopeEndpointGroup, ReuseScopeSite)
	windows := make(map[durationx.Duration]int, len(r.Quota))
	for i, q := range r.Quota {
		qp := item(field(path, "quota"), i)
		v.minInt(field(qp, "limit"), q.Limit, 1)
		v.duration(field(qp, "window"), q.Window, time.Second, 0)
		if prev, dup := windows[q.Window]; dup {
			v.addf(field(qp, "window"), "duplicates the window of quota[%d] (%s)", prev, q.Window)
		} else {
			windows[q.Window] = i
		}
	}
	v.duration(field(path, "sticky.ttl"), r.Sticky.TTL, time.Second, 0)
	v.duration(field(path, "warmup.duration"), r.Warmup.Duration, 0, 0)
	v.ratio(field(path, "warmup.quota_factor"), r.Warmup.QuotaFactor, true)
	v.ratio(field(path, "probe.weight_factor"), r.Probe.WeightFactor, true)
	v.minInt(field(path, "probe.max_leases"), int64(r.Probe.MaxLeases), 1)
}

func (p *ProxySpec) validate(v *validator, path string) {
	v.enum(field(path, "mode"), p.Mode, ProxyModeNone, ProxyModePool, ProxyModeBindIdentity, ProxyModeRegionMatch)
	v.stringList(field(path, "kinds"), p.Kinds, func(ip, k string) {
		v.enum(ip, k, ProxyKindDatacenter, ProxyKindResidential, ProxyKindMobile, ProxyKindTunnel)
	})
	v.stringList(field(path, "tags"), p.Tags, nil)
	v.stringList(field(path, "providers"), p.Providers, nil)
	v.stringList(field(path, "regions"), p.Regions, nil)
	v.duration(field(path, "rebind_tolerance"), p.RebindTolerance, 0, 0)
	v.minInt(field(path, "max_rebinds_per_day"), int64(p.MaxRebindsPerDay), 0)
}

// nilIfEmpty normalizes an empty optional list to nil.
func nilIfEmpty(l StringList) StringList {
	if len(l) == 0 {
		return nil
	}
	return l
}

func setString(dst *string, def string) {
	if *dst == "" {
		*dst = def
	}
}

func setInt(dst *int, def int) {
	if *dst == 0 {
		*dst = def
	}
}

func setDuration(dst *durationx.Duration, def time.Duration) {
	if *dst == 0 {
		*dst = durationx.Duration(def)
	}
}
