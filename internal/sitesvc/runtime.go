package sitesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/events"
)

// Runtime config kinds whose content (the reserved "_runtime" config group,
// rendered by the breaker service) lists sites and endpoint groups by name.
// They mirror breaker.KindBreakers and breaker.KindSiteSwitches.
const (
	runtimeKindBreakers     = "breakers"
	runtimeKindSiteSwitches = "site_switches"
	// runtimeEventType mirrors breaker.RuntimeEventType.
	runtimeEventType = "runtime"
)

// runtimeKinds lists the runtime kinds bumped when sites or endpoint groups
// appear or disappear.
var runtimeKinds = [...]string{runtimeKindBreakers, runtimeKindSiteSwitches}

// runtimeEventData is the data of events.ChannelRuntime events:
// {"ns","kind","version"}.
type runtimeEventData struct {
	Namespace string `json:"ns"`
	Kind      string `json:"kind"`
	Version   int64  `json:"version"`
}

// Option configures a Service.
type Option func(*Service)

// WithEventBus publishes runtime version bumps on events.ChannelRuntime so
// that "_runtime" config watchers on every instance refresh. Without a bus the
// versions are still incremented in Redis, and watchers notice the change on
// their next poll.
func WithEventBus(bus events.Bus) Option {
	return func(s *Service) { s.bus = bus }
}

// bumpRuntime increments the "breakers" and "site_switches" runtime versions
// of the namespace (HINCRBY Keys.RuntimeVersions) and publishes one runtime
// event per kind. It is called after a committed change that creates or
// deletes a site or endpoint group. Without a Redis client it does nothing.
func (s *Service) bumpRuntime(ctx context.Context, tenantID, namespaceID string) error {
	if s.rdb == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTimeout)
	defer cancel()
	key := s.keys.RuntimeVersions(namespaceID)
	cmds := make(rueidis.Commands, 0, len(runtimeKinds))
	for _, kind := range runtimeKinds {
		cmds = append(cmds, s.rdb.B().Hincrby().Key(key).Field(kind).Increment(1).Build())
	}
	results := s.rdb.DoMulti(ctx, cmds...)
	var errs []error
	for i, kind := range runtimeKinds {
		version, err := results[i].AsInt64()
		if err != nil {
			errs = append(errs, fmt.Errorf("bump runtime version %s of namespace %s: %w", kind, namespaceID, err))
			continue
		}
		if err := s.publishRuntime(ctx, tenantID, namespaceID, kind, version); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	err := errors.Join(errs...)
	s.logger.Error("runtime version bump after site change failed",
		slog.String("namespace_id", namespaceID), slog.Any("error", err))
	return err
}

// publishRuntime publishes one runtime version event (no-op without a bus).
func (s *Service) publishRuntime(ctx context.Context, tenantID, namespaceID, kind string, version int64) error {
	if s.bus == nil {
		return nil
	}
	data, err := json.Marshal(runtimeEventData{Namespace: namespaceID, Kind: kind, Version: version})
	if err != nil {
		return fmt.Errorf("marshal runtime event: %w", err)
	}
	ev := events.Event{Type: runtimeEventType, TenantID: tenantID, NamespaceID: namespaceID, At: s.now().UTC(), Data: data}
	if err := s.bus.Publish(ctx, events.ChannelRuntime, ev); err != nil {
		return fmt.Errorf("publish runtime event %s of namespace %s: %w", kind, namespaceID, err)
	}
	return nil
}
