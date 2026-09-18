package configcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter/configdb"
	"github.com/TikHub/Spinneret/internal/events"
)

// Event types used on the config channel. The namespace channel only carries
// events.TypeConfigPublished.
const (
	EventTypeConfigDeleted = "config.deleted"
	publishTimeout         = 5 * time.Second
	versionQueryChunk      = 1_000
)

// ConfigEventData is the payload of events.ChannelConfig events. Version 0
// announces a deleted item.
type ConfigEventData struct {
	NS      string `json:"ns"`
	Group   string `json:"group"`
	Key     string `json:"key"`
	Version int32  `json:"version"`
}

// RuntimeEventData is the payload of events.ChannelRuntime events (published
// by the breaker service).
type RuntimeEventData struct {
	NS      string `json:"ns"`
	Kind    string `json:"kind"`
	Version int64  `json:"version"`
}

// PublishedEventData is the payload of config.published events on the
// namespace channel. SourceVersion is set for rollbacks.
type PublishedEventData struct {
	ItemID        string `json:"item_id"`
	Group         string `json:"group"`
	Key           string `json:"key"`
	Version       int32  `json:"version"`
	SourceVersion int32  `json:"source_version,omitempty"`
	Actor         string `json:"actor"`
}

func (s *Service) onConfigEvent(_ context.Context, _ string, ev events.Event) {
	var d ConfigEventData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.NS == "" || d.Group == "" || d.Key == "" {
		return
	}
	s.hub.invalidate(itemKey{ns: d.NS, group: d.Group, key: d.Key})
}

func (s *Service) onRuntimeEvent(_ context.Context, _ string, ev events.Event) {
	var d RuntimeEventData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.NS == "" || !validRuntimeKind(d.Kind) {
		return
	}
	s.hub.invalidate(itemKey{ns: d.NS, group: RuntimeGroup, key: d.Kind})
}

// announcePublished invalidates local watch state and publishes the config
// channel event and the namespace console event of a new version.
func (s *Service) announcePublished(ctx context.Context, p *authz.Principal, h itemHeader, version, sourceVersion int32) {
	s.hub.invalidate(itemKey{ns: h.NS.ID, group: h.Group, key: h.Key})
	s.publish(ctx, events.ChannelConfig, events.Event{
		Type:        events.TypeConfigPublished,
		TenantID:    h.NS.TenantID,
		NamespaceID: h.NS.ID,
	}, ConfigEventData{NS: h.NS.ID, Group: h.Group, Key: h.Key, Version: version})
	s.publish(ctx, events.NamespaceChannel(h.NS.ID), events.Event{
		Type:        events.TypeConfigPublished,
		TenantID:    h.NS.TenantID,
		NamespaceID: h.NS.ID,
	}, PublishedEventData{
		ItemID: h.ID, Group: h.Group, Key: h.Key, Version: version, SourceVersion: sourceVersion, Actor: p.Actor(),
	})
}

// announceDeleted invalidates local watch state and publishes the deletion on
// the config channel.
func (s *Service) announceDeleted(ctx context.Context, h itemHeader) {
	s.hub.invalidate(itemKey{ns: h.NS.ID, group: h.Group, key: h.Key})
	s.publish(ctx, events.ChannelConfig, events.Event{
		Type:        EventTypeConfigDeleted,
		TenantID:    h.NS.TenantID,
		NamespaceID: h.NS.ID,
	}, ConfigEventData{NS: h.NS.ID, Group: h.Group, Key: h.Key})
}

// publish sends a best-effort bus event; failures are logged only. The
// request context's cancellation is ignored because the change is committed.
func (s *Service) publish(ctx context.Context, channel string, ev events.Event, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		s.logger.Error("encode config event failed", slog.String("channel", channel), slog.Any("error", err))
		return
	}
	ev.Data = raw
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if err := s.bus.Publish(pctx, channel, ev); err != nil {
		s.logger.Warn("publish config event failed",
			slog.String("channel", channel), slog.String("type", ev.Type), slog.Any("error", err))
	}
}

// loadVersions is the hub's version loader: stored items from PostgreSQL
// (chunked per namespace), runtime items from the RuntimeProvider. Malformed
// names and unknown system groups do not exist.
func (s *Service) loadVersions(ctx context.Context, keys []itemKey) (map[itemKey]itemVersion, error) {
	out := make(map[itemKey]itemVersion, len(keys))
	byNS := make(map[string][]itemKey)
	for _, k := range keys {
		switch {
		case k.group == RuntimeGroup:
			if s.runtime == nil || !validRuntimeKind(k.key) {
				continue
			}
			v, err := s.runtime.RuntimeVersion(ctx, k.ns, k.key)
			if err != nil {
				return nil, fmt.Errorf("read runtime config version %s: %w", k.key, err)
			}
			out[k] = itemVersion{version: apiRuntimeVersion(v)}
		case reservedGroup(k.group) || !wellFormedRef(k.group, k.key):
			continue
		default:
			byNS[k.ns] = append(byNS[k.ns], k)
		}
	}
	q := s.queries()
	for nsID, list := range byNS {
		for start := 0; start < len(list); start += versionQueryChunk {
			chunk := list[start:min(start+versionQueryChunk, len(list))]
			groups := make([]string, len(chunk))
			names := make([]string, len(chunk))
			for i, k := range chunk {
				groups[i], names[i] = k.group, k.key
			}
			rows, err := q.ConfigCurrentVersions(ctx, configdb.ConfigCurrentVersionsParams{
				GroupNames: groups, ItemKeys: names, NamespaceID: nsID,
			})
			if err != nil {
				return nil, fmt.Errorf("load config versions: %w", err)
			}
			for _, r := range rows {
				if r.CurrentVersion > 0 {
					out[itemKey{ns: nsID, group: r.GroupName, key: r.Key}] = itemVersion{id: r.ID, version: r.CurrentVersion}
				}
			}
		}
	}
	return out, nil
}

// headerOf builds an item header from a namespace and names.
func headerOf(ns *catalog.Namespace, id, group, key, format string) itemHeader {
	return itemHeader{ID: id, Group: group, Key: key, Format: format, NS: ns}
}
