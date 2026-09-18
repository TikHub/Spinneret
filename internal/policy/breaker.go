package policy

import (
	"time"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

// Breaker defaults and limits (spec §7).
const (
	DefaultBreakerWindow                 = 60 * time.Second
	DefaultBreakerBuckets                = 12
	DefaultBreakerMinRequests            = 50
	DefaultTripRiskRatioGte              = 0.4
	DefaultTripDistinctCaptchaIdentities = 10
	DefaultTripSuccessRatioLte           = 0.2
	DefaultOpenDuration                  = 2 * time.Minute
	DefaultMaxOpenDuration               = time.Hour
	DefaultResetOpenCountAfter           = 30 * time.Minute
	DefaultProbeLeasesPer10s             = 5
	DefaultCloseMinSamples               = 5
	DefaultCloseSuccessRatioGte          = 0.8

	maxBreakerBuckets = 60
)

// BreakerSpec is a breaker policy for an endpoint group.
type BreakerSpec struct {
	Name                  string             `yaml:"name" json:"name"`
	Description           string             `yaml:"description,omitempty" json:"description,omitempty"`
	Bind                  *Binding           `yaml:"bind,omitempty" json:"bind,omitempty"`
	Enabled               bool               `yaml:"enabled" json:"enabled"`
	Window                durationx.Duration `yaml:"window" json:"window"`
	Buckets               int                `yaml:"buckets" json:"buckets"`
	MinRequests           int                `yaml:"min_requests" json:"min_requests"`
	Trip                  TripSpec           `yaml:"trip" json:"trip"`
	OpenDuration          durationx.Duration `yaml:"open_duration" json:"open_duration"`
	MaxOpenDuration       durationx.Duration `yaml:"max_open_duration" json:"max_open_duration"`
	ResetOpenCountAfter   durationx.Duration `yaml:"reset_open_count_after" json:"reset_open_count_after"`
	HalfOpen              HalfOpenSpec       `yaml:"half_open" json:"half_open"`
	RevertRecentCooldowns RevertMode         `yaml:"revert_recent_cooldowns" json:"revert_recent_cooldowns"`
}

// TripSpec lists the trip conditions; any satisfied condition opens the
// breaker and a zero value disables that condition.
type TripSpec struct {
	RiskRatioGte                 float64 `yaml:"risk_ratio_gte" json:"risk_ratio_gte"`
	DistinctCaptchaIdentitiesGte int     `yaml:"distinct_captcha_identities_gte" json:"distinct_captcha_identities_gte"`
	SuccessRatioLte              float64 `yaml:"success_ratio_lte" json:"success_ratio_lte"`
}

// HalfOpenSpec configures probing while half-open.
type HalfOpenSpec struct {
	ProbeLeasesPer10s    int     `yaml:"probe_leases_per_10s" json:"probe_leases_per_10s"`
	CloseMinSamples      int     `yaml:"close_min_samples" json:"close_min_samples"`
	CloseSuccessRatioGte float64 `yaml:"close_success_ratio_gte" json:"close_success_ratio_gte"`
}

// newBreakerBase returns an unnamed breaker spec carrying every default.
func newBreakerBase() *BreakerSpec {
	s := &BreakerSpec{
		Enabled: true,
		Trip: TripSpec{
			RiskRatioGte:                 DefaultTripRiskRatioGte,
			DistinctCaptchaIdentitiesGte: DefaultTripDistinctCaptchaIdentities,
			SuccessRatioLte:              DefaultTripSuccessRatioLte,
		},
	}
	s.ApplyDefaults()
	return s
}

// Kind implements Spec.
func (s *BreakerSpec) Kind() Kind { return KindBreaker }

// PolicyName implements Spec.
func (s *BreakerSpec) PolicyName() string {
	if s == nil {
		return ""
	}
	return s.Name
}

// PolicyBinding implements Spec.
func (s *BreakerSpec) PolicyBinding() *Binding {
	if s == nil {
		return nil
	}
	return copyBinding(s.Bind)
}

// BucketDuration returns Window / Buckets (0 when Buckets is not positive).
func (s *BreakerSpec) BucketDuration() time.Duration {
	if s == nil || s.Buckets <= 0 {
		return 0
	}
	return s.Window.Std() / time.Duration(s.Buckets)
}

// ApplyDefaults fills zero values whose zero is not a legal setting. enabled
// and the trip thresholds accept false/0 and are therefore only defaulted by
// the parser.
func (s *BreakerSpec) ApplyDefaults() {
	if s == nil {
		return
	}
	setDuration(&s.Window, DefaultBreakerWindow)
	setInt(&s.Buckets, DefaultBreakerBuckets)
	setInt(&s.MinRequests, DefaultBreakerMinRequests)
	setDuration(&s.OpenDuration, DefaultOpenDuration)
	setDuration(&s.MaxOpenDuration, DefaultMaxOpenDuration)
	setDuration(&s.ResetOpenCountAfter, DefaultResetOpenCountAfter)
	setInt(&s.HalfOpen.ProbeLeasesPer10s, DefaultProbeLeasesPer10s)
	setInt(&s.HalfOpen.CloseMinSamples, DefaultCloseMinSamples)
	if s.HalfOpen.CloseSuccessRatioGte == 0 {
		s.HalfOpen.CloseSuccessRatioGte = DefaultCloseSuccessRatioGte
	}
	if s.RevertRecentCooldowns == "" {
		s.RevertRecentCooldowns = RevertEndpoint
	}
}

// Validate implements Spec.
func (s *BreakerSpec) Validate() error {
	if s == nil {
		return &ValidationError{Kind: KindBreaker, Problems: []Problem{{Message: "spec is nil"}}}
	}
	v := &validator{}
	validateCommon(v, s.Name, s.Description, s.Bind)
	v.duration("window", s.Window, time.Second, 0)
	v.intRange("buckets", int64(s.Buckets), 1, maxBreakerBuckets)
	if !s.Window.IsPermanent() && s.Window.Std() >= time.Second && s.Buckets >= 1 && s.Buckets <= maxBreakerBuckets {
		windowMs := s.Window.Milliseconds()
		switch {
		case windowMs%int64(s.Buckets) != 0:
			v.addf("buckets", "must divide window (%s) into whole milliseconds", s.Window)
		case windowMs/int64(s.Buckets) < time.Second.Milliseconds():
			v.addf("buckets", "must divide window (%s) into buckets of at least 1s", s.Window)
		}
	}
	v.minInt("min_requests", int64(s.MinRequests), 1)
	v.ratio("trip.risk_ratio_gte", s.Trip.RiskRatioGte, false)
	v.minInt("trip.distinct_captcha_identities_gte", int64(s.Trip.DistinctCaptchaIdentitiesGte), 0)
	v.ratio("trip.success_ratio_lte", s.Trip.SuccessRatioLte, false)
	if s.Enabled && s.Trip.RiskRatioGte == 0 && s.Trip.DistinctCaptchaIdentitiesGte == 0 && s.Trip.SuccessRatioLte == 0 {
		v.addf("trip", "at least one condition must be non-zero while the breaker is enabled")
	}
	v.duration("open_duration", s.OpenDuration, time.Second, 0)
	v.duration("max_open_duration", s.MaxOpenDuration, time.Second, 0)
	if !s.OpenDuration.IsPermanent() && !s.MaxOpenDuration.IsPermanent() && s.MaxOpenDuration < s.OpenDuration {
		v.addf("max_open_duration", "must be at least open_duration (%s)", s.OpenDuration)
	}
	v.duration("reset_open_count_after", s.ResetOpenCountAfter, time.Second, 0)
	v.minInt("half_open.probe_leases_per_10s", int64(s.HalfOpen.ProbeLeasesPer10s), 1)
	v.minInt("half_open.close_min_samples", int64(s.HalfOpen.CloseMinSamples), 1)
	v.ratio("half_open.close_success_ratio_gte", s.HalfOpen.CloseSuccessRatioGte, true)
	if !ValidRevertMode(s.RevertRecentCooldowns) {
		v.addf("revert_recent_cooldowns", "must be one of none|endpoint|all or a boolean (got %q)", s.RevertRecentCooldowns)
	}
	return v.result(KindBreaker, s.Name)
}
