package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

// busEmitTimeout bounds the handling of one bus event.
const busEmitTimeout = 10 * time.Second

// State names used by bus events.
const (
	breakerClosed   = "closed"
	breakerOpen     = "open"
	breakerHalfOpen = "half_open"
	identityExpired = "expired"
)

// busItem is a bus event queued for alert conversion.
type busItem struct {
	channel string
	ev      events.Event
}

// onBusEvent is the bus handler. It only filters and enqueues (bus handlers
// must not block); conversion happens on the bus loop goroutine. Only
// identity.state events that can alert (transitions to expired) take queue
// slots, so a burst of other lifecycle changes cannot crowd them out. Events
// dropped because the queue is full are recovered from state_events by the
// evaluation job (see recoverExpiredIdentities).
func (s *Service) onBusEvent(_ context.Context, channel string, ev events.Event) {
	if !strings.HasPrefix(channel, "ns:") {
		return
	}
	switch ev.Type {
	case events.TypeBreakerTransition:
	case events.TypeIdentityState:
		if !isExpiryEvent(ev) {
			return
		}
	default:
		return
	}
	select {
	case s.busQueue <- busItem{channel: channel, ev: ev}:
	default:
		if n := s.droppedBusEvents.Add(1); n%100 == 1 {
			s.logger.Error("notify bus queue full, dropping events", slog.Int64("dropped_total", n))
		}
	}
}

// busLoop converts queued bus events into alerts until ctx is canceled.
func (s *Service) busLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.busQueue:
			s.handleBusItem(ctx, item)
		}
	}
}

func (s *Service) handleBusItem(ctx context.Context, item busItem) {
	ctx, cancel := context.WithTimeout(ctx, busEmitTimeout)
	defer cancel()
	var (
		a   Alert
		ok  bool
		err error
	)
	switch item.ev.Type {
	case events.TypeBreakerTransition:
		a, ok, err = s.breakerAlert(item.channel, item.ev)
	case events.TypeIdentityState:
		a, ok, err = s.identityExpiredAlert(ctx, item.channel, item.ev)
	}
	if err == nil && ok {
		err = s.Emit(ctx, a)
	}
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("bus event alert failed", slog.String("event_type", item.ev.Type), slog.Any("error", err))
	}
}

// flexTime decodes an RFC 3339 string or Unix milliseconds (integer or
// float). Invalid or null values decode to the zero time instead of failing
// the whole event, because the field is informational.
type flexTime struct{ time.Time }

// UnmarshalJSON implements json.Unmarshaler.
func (t *flexTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
	case b[0] == '"':
		var s string
		if json.Unmarshal(b, &s) == nil {
			if parsed, err := time.Parse(time.RFC3339Nano, s); err == nil {
				t.Time = parsed
			}
		}
	default:
		if ms, err := strconv.ParseFloat(string(b), 64); err == nil && ms > 0 && ms < 1e15 {
			t.Time = time.UnixMilli(int64(ms))
		}
	}
	return nil
}

// breakerTransition is the data of a breaker.transition event.
type breakerTransition struct {
	SiteID          string         `json:"site_id"`
	Site            string         `json:"site"`
	EndpointGroupID string         `json:"endpoint_group_id"`
	EndpointGroup   string         `json:"endpoint_group"`
	Client          string         `json:"client"`
	From            string         `json:"from"`
	To              string         `json:"to"`
	FromState       string         `json:"from_state"`
	ToState         string         `json:"to_state"`
	Trigger         string         `json:"trigger"`
	Reason          string         `json:"reason"`
	OpenUntil       flexTime       `json:"open_until"`
	Version         int64          `json:"version"`
	V               int64          `json:"v"`
	Metrics         map[string]any `json:"metrics"`
}

// namespaceOf resolves the namespace of a bus event.
func (s *Service) namespaceOf(channel string, ev events.Event) (*catalog.Namespace, bool) {
	nsID := ev.NamespaceID
	if nsID == "" {
		nsID = strings.TrimPrefix(channel, "ns:")
	}
	return s.cat.Namespace(nsID)
}

// breakerAlert converts a breaker transition into an alert.
func (s *Service) breakerAlert(channel string, ev events.Event) (Alert, bool, error) {
	var d breakerTransition
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return Alert{}, false, fmt.Errorf("decode breaker transition: %w", err)
	}
	from, to := firstNonEmpty(d.From, d.FromState), firstNonEmpty(d.To, d.ToState)
	var kind, severity, verb string
	switch {
	case to == breakerOpen && from == breakerHalfOpen:
		kind, severity, verb = KindBreakerReopened, SeverityCritical, "reopened"
	case to == breakerOpen && from != breakerOpen:
		kind, severity, verb = KindBreakerOpened, SeverityCritical, "opened"
	case to == breakerClosed && (from == breakerOpen || from == breakerHalfOpen):
		kind, severity, verb = KindBreakerClosed, SeverityInfo, "closed"
	default:
		return Alert{}, false, nil
	}
	ns, ok := s.namespaceOf(channel, ev)
	if !ok {
		return Alert{}, false, nil
	}
	siteID := firstNonEmpty(d.SiteID, ev.SiteID)
	siteName, groupName, client := d.Site, d.EndpointGroup, d.Client
	if site, ok := ns.SitesByID[siteID]; ok {
		siteName = site.Name
		if g, ok := site.GroupsByID[d.EndpointGroupID]; ok {
			groupName, client = g.Name, g.Client
		}
	}
	if siteID == "" {
		return Alert{}, false, nil
	}
	subject := strings.Trim(siteName+"/"+client+"/"+groupName, "/")
	details := map[string]any{
		"site": siteName, "site_id": siteID, "endpoint_group": groupName, "endpoint_group_id": d.EndpointGroupID,
		"client": client, "from": from, "to": to, "trigger": d.Trigger, "reason": d.Reason,
	}
	message := fmt.Sprintf("Circuit breaker of %s %s (%s → %s", subject, verb, from, to)
	if d.Trigger != "" {
		message += ", trigger " + d.Trigger
	}
	message += ")."
	if d.Reason != "" {
		message += " Reason: " + d.Reason + "."
	}
	if !d.OpenUntil.IsZero() && to == breakerOpen {
		details["open_until"] = d.OpenUntil.UTC().Format(time.RFC3339)
		message += " Open until " + d.OpenUntil.UTC().Format(time.RFC3339) + "."
	}
	if len(d.Metrics) > 0 {
		details["metrics"] = d.Metrics
	}
	version := max(d.Version, d.V)
	marker := strconv.FormatInt(version, 10)
	if version == 0 {
		marker = "t" + strconv.FormatInt(ev.At.UnixMilli(), 10)
	}
	return Alert{
		Kind: kind, Severity: severity, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: siteID,
		Title:    "Breaker " + verb + ": " + subject,
		Message:  message,
		Details:  details,
		DedupKey: "breaker:" + firstNonEmpty(d.EndpointGroupID, siteID) + ":" + from + ">" + to + ":" + marker,
	}, true, nil
}

// stateChange is the data of an identity.state event.
type stateChange struct {
	SubjectKind string   `json:"subject_kind"`
	SubjectID   string   `json:"subject_id"`
	SiteID      string   `json:"site_id"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Action      string   `json:"action"`
	Until       flexTime `json:"until"`
	Reason      string   `json:"reason"`
}

// isExpiry reports whether a decoded identity.state change is an identity
// transition to "expired".
func (d stateChange) isExpiry() bool {
	return (d.SubjectKind == "" || d.SubjectKind == "identity") && d.To == identityExpired && d.From != identityExpired &&
		d.SubjectID != ""
}

// isExpiryEvent reports whether an identity.state event can alert. Malformed
// data is queued so that the bus loop logs it.
func isExpiryEvent(ev events.Event) bool {
	var d stateChange
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return true
	}
	return d.isExpiry()
}

// identityExpiredAlert converts an identity state change to "expired" into
// an identity_expired alert (consumed by refresh services through webhooks).
func (s *Service) identityExpiredAlert(ctx context.Context, channel string, ev events.Event) (Alert, bool, error) {
	var d stateChange
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return Alert{}, false, fmt.Errorf("decode identity state change: %w", err)
	}
	if !d.isExpiry() {
		return Alert{}, false, nil
	}
	ns, ok := s.namespaceOf(channel, ev)
	if !ok {
		return Alert{}, false, nil
	}
	var ref *identityRef
	row, err := s.q.NotifyIdentityRef(ctx, d.SubjectID)
	switch {
	case err == nil:
		ref = &identityRef{siteID: row.SiteID, client: row.Client, typeID: row.TypeID}
	case !errors.Is(err, pgx.ErrNoRows):
		return Alert{}, false, fmt.Errorf("load identity %s: %w", d.SubjectID, err)
	}
	a, ok := expiredAlert(ns, expiredTransition{
		identityID: d.SubjectID, siteID: firstNonEmpty(d.SiteID, ev.SiteID), from: d.From, reason: d.Reason, at: ev.At,
	}, ref)
	return a, ok, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
