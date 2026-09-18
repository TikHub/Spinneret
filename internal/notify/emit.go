package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/notify/notifydb"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// dedupTimeout bounds the Redis round trip of alert de-duplication.
const dedupTimeout = 3 * time.Second

// Emit stores an alert and schedules its deliveries. An alert whose
// de-duplication key was seen within its TTL is silently dropped. Deliveries
// run asynchronously on the workers started by Run.
func (s *Service) Emit(ctx context.Context, a Alert) error {
	_, err := s.emit(ctx, a)
	return err
}

// emit implements Emit and returns the stored alert ID ("" when suppressed).
func (s *Service) emit(ctx context.Context, a Alert) (string, error) {
	a, err := normalizeAlert(a)
	if err != nil {
		return "", err
	}
	dedupKey := ""
	if a.DedupKey != "" {
		dedupKey = s.dedupKeyOf(a)
		fresh, err := s.claimDedup(ctx, dedupKey, a.DedupTTL)
		if err != nil {
			return "", err
		}
		if !fresh {
			return "", nil
		}
	}
	ev, err := s.insertAlert(ctx, a)
	if err != nil {
		if dedupKey != "" {
			s.releaseDedup(ctx, dedupKey)
		}
		return "", err
	}
	s.publishAlert(ctx, ev)
	targets, err := s.matchTargets(ctx, a)
	if err != nil {
		// The alert is stored; failing to load channels must not make callers
		// retry (which the de-duplication key would suppress anyway).
		s.logger.Error("match alert channels failed", slog.String("alert_id", ev.ID), slog.Any("error", err))
		return ev.ID, nil
	}
	if len(targets) == 0 {
		return ev.ID, nil
	}
	msg := s.messageOf(ctx, ev)
	for _, t := range targets {
		s.enqueue(deliveryJob{alertID: ev.ID, msg: msg, target: t})
	}
	return ev.ID, nil
}

// normalizeAlert validates an alert and applies defaults and size limits.
func normalizeAlert(a Alert) (Alert, error) {
	switch {
	case a.TenantID == "":
		return a, apperr.InvalidArgument("", "alert tenant is required")
	case !ValidKind(a.Kind):
		return a, apperr.InvalidArgument("", "unknown alert kind %q", truncateBytes(a.Kind, 64))
	case !ValidSeverity(a.Severity):
		return a, apperr.InvalidArgument("", "unknown alert severity %q", truncateBytes(a.Severity, 32))
	case a.SiteID != "" && a.NamespaceID == "":
		return a, apperr.InvalidArgument("", "a site alert requires a namespace")
	case len(a.DedupKey) > maxDedupKeyBytes:
		return a, apperr.InvalidArgument("", "alert dedup key must be at most %d bytes", maxDedupKeyBytes)
	}
	a.Title = ellipsis(oneLine(validUTF8(strings.TrimSpace(a.Title))), MaxTitleBytes)
	if a.Title == "" {
		return a, apperr.InvalidArgument("", "alert title is required")
	}
	a.Message = ellipsis(validUTF8(a.Message), MaxMessageBytes)
	if a.DedupTTL <= 0 {
		a.DedupTTL = DefaultDedupTTL
	}
	return a, nil
}

// dedupKeyOf returns the Redis key of an alert's de-duplication marker.
func (s *Service) dedupKeyOf(a Alert) string {
	return s.keys.AlertDedup(a.TenantID + ":" + a.Kind + ":" + a.DedupKey)
}

// claimDedup sets the de-duplication marker; false means a recent identical
// alert exists.
func (s *Service) claimDedup(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	dctx, cancel := context.WithTimeout(ctx, dedupTimeout)
	defer cancel()
	err := s.rdb.Do(dctx, s.rdb.B().Set().Key(key).Value(s.now().UTC().Format(time.RFC3339)).Nx().
		PxMilliseconds(max(ttl.Milliseconds(), 1)).Build()).Error()
	switch {
	case err == nil:
		return true, nil
	case rueidis.IsRedisNil(err):
		return false, nil
	default:
		return false, fmt.Errorf("claim alert dedup key: %w", err)
	}
}

// releaseDedup removes a marker so a failed emission can be retried.
func (s *Service) releaseDedup(ctx context.Context, key string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dedupTimeout)
	defer cancel()
	if err := s.rdb.Do(dctx, s.rdb.B().Del().Key(key).Build()).Error(); err != nil {
		s.logger.Warn("release alert dedup key failed", slog.Any("error", err))
	}
}

// insertAlert stores the alert row.
func (s *Service) insertAlert(ctx context.Context, a Alert) (AlertEvent, error) {
	details := []byte("{}")
	if len(a.Details) > 0 {
		b, err := json.Marshal(a.Details)
		if err != nil {
			return AlertEvent{}, apperr.InvalidArgument("", "alert details are not JSON encodable")
		}
		if len(b) > MaxDetailsBytes {
			return AlertEvent{}, apperr.InvalidArgument("", "alert details exceed %d bytes", MaxDetailsBytes)
		}
		details = b
	}
	row, err := s.q.NotifyAlertInsert(ctx, notifydb.NotifyAlertInsertParams{
		ID:          idgen.New(idgen.Alert),
		CreatedAt:   s.now().UTC(),
		TenantID:    a.TenantID,
		NamespaceID: a.NamespaceID,
		SiteID:      a.SiteID,
		Kind:        a.Kind,
		Severity:    a.Severity,
		Title:       a.Title,
		Message:     a.Message,
		Details:     details,
		DedupKey:    a.DedupKey,
	})
	if err != nil {
		return AlertEvent{}, fmt.Errorf("insert alert: %w", err)
	}
	return alertEventOf(row), nil
}

// alertEventData is the payload of the "alert" bus event.
type alertEventData struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	NamespaceID string    `json:"namespace_id,omitempty"`
	SiteID      string    `json:"site_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// publishAlert announces an alert on its namespace channel, or on every
// namespace channel of the tenant for tenant-level alerts (best effort).
func (s *Service) publishAlert(ctx context.Context, ev AlertEvent) {
	if s.bus == nil {
		return
	}
	data, err := json.Marshal(alertEventData{
		ID: ev.ID, Kind: ev.Kind, Severity: ev.Severity, Title: ev.Title, Message: ev.Message,
		NamespaceID: ev.NamespaceID, SiteID: ev.SiteID, CreatedAt: ev.CreatedAt,
	})
	if err != nil {
		return
	}
	namespaces := []string{ev.NamespaceID}
	if ev.NamespaceID == "" {
		namespaces = namespaces[:0]
		for _, ns := range s.cat.Namespaces(ev.TenantID) {
			namespaces = append(namespaces, ns.ID)
		}
	}
	for _, nsID := range namespaces {
		err := s.bus.Publish(ctx, events.NamespaceChannel(nsID), events.Event{
			Type: events.TypeAlert, TenantID: ev.TenantID, NamespaceID: nsID, SiteID: ev.SiteID,
			At: ev.CreatedAt, Data: data,
		})
		if err != nil {
			s.logger.Warn("publish alert event failed", slog.String("alert_id", ev.ID), slog.Any("error", err))
		}
	}
}

// messageOf builds the delivery content of a stored alert, resolving names.
func (s *Service) messageOf(ctx context.Context, ev AlertEvent) message {
	m := message{
		ID: ev.ID, Kind: ev.Kind, Severity: ev.Severity, Title: ev.Title, Message: ev.Message,
		Details: ev.Details, CreatedAt: ev.CreatedAt,
	}
	if m.Details == nil {
		m.Details = map[string]any{}
	}
	if ns, ok := s.cat.Namespace(ev.NamespaceID); ok && ev.NamespaceID != "" {
		m.Namespace = ns.Name
		m.Tenant = ns.TenantName
		if site, ok := ns.SitesByID[ev.SiteID]; ok && ev.SiteID != "" {
			m.Site = site.Name
		}
	}
	if m.Tenant == "" {
		m.Tenant = s.tenantName(ctx, ev.TenantID)
	}
	return m
}

// tenantName resolves a tenant name from the catalog or the database; it
// falls back to the tenant ID.
func (s *Service) tenantName(ctx context.Context, tenantID string) string {
	for _, ns := range s.cat.Namespaces(tenantID) {
		if ns.TenantName != "" {
			return ns.TenantName
		}
	}
	name, err := s.q.NotifyTenantName(ctx, tenantID)
	if err != nil || name == "" {
		return tenantID
	}
	return name
}

// alertEventOf converts a row.
func alertEventOf(row notifydb.AlertEvent) AlertEvent {
	ev := AlertEvent{
		ID: row.ID, CreatedAt: row.CreatedAt, TenantID: row.TenantID, NamespaceID: row.NamespaceID,
		SiteID: row.SiteID, Kind: row.Kind, Severity: row.Severity, Title: row.Title, Message: row.Message,
		DedupKey: row.DedupKey, Details: map[string]any{}, Deliveries: []Delivery{},
	}
	if len(row.Details) > 0 {
		_ = json.Unmarshal(row.Details, &ev.Details)
	}
	if len(row.Deliveries) > 0 {
		_ = json.Unmarshal(row.Deliveries, &ev.Deliveries)
	}
	if ev.Details == nil {
		ev.Details = map[string]any{}
	}
	if ev.Deliveries == nil {
		ev.Deliveries = []Delivery{}
	}
	return ev
}

// ListAlertEvents returns a page of alert events, newest first.
func (s *Service) ListAlertEvents(ctx context.Context, query AlertQuery) (AlertPage, error) {
	if query.TenantID == "" {
		return AlertPage{}, apperr.InvalidArgument("", "tenant is required")
	}
	nsIDs, siteIDs := nonNil(query.NamespaceIDs), nonNil(query.SiteIDs)
	if !query.IncludeTenant && len(nsIDs) == 0 && len(siteIDs) == 0 {
		return AlertPage{Events: []AlertEvent{}}, nil
	}
	if (query.Kind != "" && !ValidKind(query.Kind)) || (query.Severity != "" && !ValidSeverity(query.Severity)) {
		return AlertPage{}, apperr.InvalidArgument("", "unknown kind or severity filter")
	}
	limit := clampLimit(query.Limit)
	params := notifydb.NotifyAlertListParams{
		TenantID:          query.TenantID,
		IncludeTenant:     query.IncludeTenant,
		NamespaceIds:      nsIDs,
		SiteIds:           siteIDs,
		FilterNamespaceID: query.NamespaceID,
		FilterSiteID:      query.SiteID,
		FilterKind:        query.Kind,
		FilterSeverity:    query.Severity,
		FromAt:            query.From,
		ToAt:              query.To,
		LimitRows:         int32(limit + 1),
	}
	if query.AfterAt != nil {
		params.CursorAt = query.AfterAt
		params.CursorID = query.AfterID
	}
	rows, err := s.q.NotifyAlertList(ctx, params)
	if err != nil {
		return AlertPage{}, fmt.Errorf("list alert events: %w", err)
	}
	page := AlertPage{More: len(rows) > limit}
	if page.More {
		rows = rows[:limit]
	}
	page.Events = make([]AlertEvent, 0, len(rows))
	for _, row := range rows {
		page.Events = append(page.Events, alertEventOf(row))
	}
	return page, nil
}
