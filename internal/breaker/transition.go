package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TikHub/Spinneret/internal/breaker/breakerdb"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// Runtime config kinds served under the reserved "_runtime" config group.
const (
	KindBreakers     = "breakers"
	KindSiteSwitches = "site_switches"
)

// RuntimeEventType is the Event.Type of runtime version bumps published on
// events.ChannelRuntime.
const RuntimeEventType = "runtime"

// actorSystem is the actor of automatic transitions.
const actorSystem = "system"

// EventMetrics is the metrics object stored in breaker_events.metrics and sent
// in TransitionData.Metrics.
type EventMetrics struct {
	Total                     int64   `json:"total"`
	Success                   int64   `json:"success"`
	Risk                      int64   `json:"risk"`
	CaptchaIdentities         int64   `json:"captcha_identities"`
	RiskRatio                 float64 `json:"risk_ratio"`
	SuccessRatio              float64 `json:"success_ratio"`
	ProbeSamples              int64   `json:"probe_samples"`
	ProbeSuccesses            int64   `json:"probe_successes"`
	RevertedEndpointCooldowns int64   `json:"reverted_endpoint_cooldowns,omitempty"`
	RevertedSiteCooldowns     int64   `json:"reverted_site_cooldowns,omitempty"`
}

// TransitionData is the data of events.TypeBreakerTransition events.
type TransitionData struct {
	Namespace        string       `json:"namespace"`
	Site             string       `json:"site"`
	SiteID           string       `json:"site_id"`
	Client           string       `json:"client"`
	EndpointGroup    string       `json:"endpoint_group"`
	EndpointGroupID  string       `json:"endpoint_group_id"`
	From             string       `json:"from"`
	To               string       `json:"to"`
	Trigger          string       `json:"trigger"`
	Reason           string       `json:"reason"`
	OpenUntil        *time.Time   `json:"open_until"`
	ConsecutiveOpens int64        `json:"consecutive_opens"`
	Manual           bool         `json:"manual"`
	Actor            string       `json:"actor"`
	Metrics          EventMetrics `json:"metrics"`
}

// RuntimeEventData is the data of runtime version events on events.ChannelRuntime.
type RuntimeEventData struct {
	Namespace string `json:"ns"`
	Kind      string `json:"kind"`
	Version   int64  `json:"version"`
}

// transitionRecord describes one breaker state change to persist and publish.
type transitionRecord struct {
	ns      *catalog.Namespace
	site    *catalog.Site
	group   *catalog.EndpointGroup
	at      time.Time
	from    string
	to      string
	trigger string
	actor   string
	hash    Hash
	window  Window
	// probeSamples and probeSuccesses are the probe statistics at the time of
	// the transition.
	probeSamples   int64
	probeSuccesses int64
	// revertedEndpoint and revertedSite count cooldowns reverted on opening.
	revertedEndpoint int64
	revertedSite     int64
}

func (r transitionRecord) metrics() EventMetrics {
	return EventMetrics{
		Total:                     r.window.Total,
		Success:                   r.window.Success,
		Risk:                      r.window.Risk,
		CaptchaIdentities:         r.window.CaptchaIdentities,
		RiskRatio:                 roundRatio(r.window.RiskRatio()),
		SuccessRatio:              roundRatio(r.window.SuccessRatio()),
		ProbeSamples:              r.probeSamples,
		ProbeSuccesses:            r.probeSuccesses,
		RevertedEndpointCooldowns: r.revertedEndpoint,
		RevertedSiteCooldowns:     r.revertedSite,
	}
}

// openUntil returns the end of the open period, nil unless the new state is a
// timed open.
func (r transitionRecord) openUntil() *time.Time {
	if r.to != StateOpen || r.hash.OpenUntilMs <= 0 {
		return nil
	}
	t := time.UnixMilli(r.hash.OpenUntilMs).UTC()
	return &t
}

// recordTransition persists the transition in breaker_events, bumps the
// "breakers" runtime version, publishes the namespace and runtime events and
// counts the transition. Every step is attempted; errors are joined.
func (s *Service) recordTransition(ctx context.Context, r transitionRecord) error {
	var errs []error
	m := r.metrics()
	metricsJSON, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal breaker metrics: %w", err)
	}
	if err := s.insertEvent(ctx, breakerdb.BreakerInsertEventParams{
		ID:              idgen.New(idgen.BreakerEvent),
		CreatedAt:       r.at.UTC(),
		TenantID:        r.ns.TenantID,
		NamespaceID:     r.ns.ID,
		SiteID:          r.site.ID,
		EndpointGroupID: r.group.ID,
		FromState:       r.from,
		ToState:         r.to,
		Trigger:         r.trigger,
		Reason:          r.hash.Reason,
		OpenUntil:       r.openUntil(),
		Metrics:         metricsJSON,
		Actor:           r.actor,
	}); err != nil {
		errs = append(errs, err)
	}
	if _, err := s.bumpRuntime(ctx, r.ns, KindBreakers); err != nil {
		errs = append(errs, err)
	}
	data := TransitionData{
		Namespace:        r.ns.Name,
		Site:             r.site.Name,
		SiteID:           r.site.ID,
		Client:           r.group.Client,
		EndpointGroup:    r.group.Name,
		EndpointGroupID:  r.group.ID,
		From:             r.from,
		To:               r.to,
		Trigger:          r.trigger,
		Reason:           r.hash.Reason,
		OpenUntil:        r.openUntil(),
		ConsecutiveOpens: r.hash.OpenCount,
		Manual:           r.hash.Manual,
		Actor:            r.actor,
		Metrics:          m,
	}
	if err := s.publishNamespace(ctx, r.ns, r.site.ID, r.at, data); err != nil {
		errs = append(errs, err)
	}
	s.observeTransition(r.site, r.group, r.to)
	s.observeState(r.site, r.group, r.hash.State)
	return errors.Join(errs...)
}

// insertEvent writes one breaker_events row with a bounded timeout.
func (s *Service) insertEvent(ctx context.Context, arg breakerdb.BreakerInsertEventParams) error {
	ictx, cancel := s.ioContext(ctx)
	defer cancel()
	if err := s.q.BreakerInsertEvent(ictx, arg); err != nil {
		return fmt.Errorf("insert breaker event for site %s group %q: %w", arg.SiteID, arg.EndpointGroupID, err)
	}
	return nil
}

// bumpRuntime increments the runtime version of kind for the namespace and
// publishes the runtime event. It returns the new version.
func (s *Service) bumpRuntime(ctx context.Context, ns *catalog.Namespace, kind string) (int64, error) {
	ictx, cancel := s.ioContext(ctx)
	defer cancel()
	version, err := s.rdb.Do(ictx, s.rdb.B().Hincrby().Key(s.keys.RuntimeVersions(ns.ID)).Field(kind).Increment(1).Build()).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("bump runtime version %s of namespace %s: %w", kind, ns.ID, err)
	}
	data, err := json.Marshal(RuntimeEventData{Namespace: ns.ID, Kind: kind, Version: version})
	if err != nil {
		return version, fmt.Errorf("marshal runtime event: %w", err)
	}
	if s.bus != nil {
		ev := events.Event{Type: RuntimeEventType, TenantID: ns.TenantID, NamespaceID: ns.ID, At: s.now().UTC(), Data: data}
		if err := s.bus.Publish(ictx, events.ChannelRuntime, ev); err != nil {
			return version, fmt.Errorf("publish runtime event %s of namespace %s: %w", kind, ns.ID, err)
		}
	}
	return version, nil
}

// publishNamespace publishes a breaker.transition event on the namespace channel.
func (s *Service) publishNamespace(ctx context.Context, ns *catalog.Namespace, siteID string, at time.Time, data TransitionData) error {
	if s.bus == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal breaker transition event: %w", err)
	}
	ev := events.Event{
		Type:        events.TypeBreakerTransition,
		TenantID:    ns.TenantID,
		NamespaceID: ns.ID,
		SiteID:      siteID,
		At:          at.UTC(),
		Data:        raw,
	}
	pctx, cancel := s.ioContext(ctx)
	defer cancel()
	if err := s.bus.Publish(pctx, events.NamespaceChannel(ns.ID), ev); err != nil {
		return fmt.Errorf("publish breaker transition of site %s: %w", siteID, err)
	}
	return nil
}

// roundRatio rounds a ratio to four decimals for storage and display.
func roundRatio(f float64) float64 {
	const scale = 10000
	return float64(int64(f*scale+0.5)) / scale
}
