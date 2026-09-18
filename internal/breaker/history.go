package breaker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakerdb"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// EventFilter selects breaker events of a namespace.
type EventFilter struct {
	// Site restricts events to one site name.
	Site string
	// EndpointGroupID restricts events to one endpoint group.
	EndpointGroupID string
	// Trigger restricts events to auto|manual|probe|site_switch ("" = all).
	Trigger string
	// Start (inclusive) and End (exclusive) bound created_at; nil = unbounded.
	Start, End *time.Time
	// PageSize is normalized with apiutil.PageSize.
	PageSize int32
	// PageToken continues a previous page.
	PageToken string
}

// Event is one stored breaker transition or site switch.
type Event struct {
	ID              string
	CreatedAt       time.Time
	SiteID          string
	Site            string
	Client          string
	EndpointGroup   string
	EndpointGroupID string
	FromState       string
	ToState         string
	Trigger         string
	Reason          string
	OpenUntil       *time.Time
	// Metrics is the decoded metrics object (EventMetrics fields for
	// transitions, empty for site switches).
	Metrics map[string]any
	Actor   string
}

// EventPage is one page of events, newest first.
type EventPage struct {
	Events        []Event
	NextPageToken string
}

// eventCursor is the keyset position of event pages.
type eventCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

var validTriggers = []string{TriggerAuto, TriggerManual, TriggerProbe, TriggerSiteSwitch}

// ListEvents lists breaker events of the sites the principal can read
// (breaker:read), newest first, with keyset pagination.
func (s *Service) ListEvents(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f EventFilter) (EventPage, error) {
	if p == nil {
		return EventPage{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if f.Trigger != "" && !slices.Contains(validTriggers, f.Trigger) {
		return EventPage{}, apperr.InvalidArgument("", "unknown trigger %q", f.Trigger)
	}
	if f.Start != nil && f.End != nil && !f.End.After(*f.Start) {
		return EventPage{}, apperr.InvalidArgument("", "time_range.end must be after time_range.start")
	}
	all, accessible := accessibleSites(p, ns, authz.PermBreakerRead)
	arg := breakerdb.BreakerListEventsParams{
		NamespaceID: ns.ID,
		AllSites:    all,
		SiteIds:     make([]string, 0, len(accessible)),
		StartAt:     f.Start,
		EndAt:       f.End,
		PageLimit:   int32(apiutil.PageSize(f.PageSize) + 1),
	}
	for id := range accessible {
		arg.SiteIds = append(arg.SiteIds, id)
	}
	slices.Sort(arg.SiteIds)
	if !all && len(arg.SiteIds) == 0 {
		if err := p.Require(authz.PermBreakerRead, namespaceResource(ns)); err != nil {
			return EventPage{}, err
		}
	}
	if f.Site != "" {
		site, ok := ns.Sites[f.Site]
		if !ok {
			return EventPage{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", f.Site)
		}
		if err := p.Require(authz.PermBreakerRead, siteResource(ns, site)); err != nil {
			return EventPage{}, err
		}
		arg.SiteID = &site.ID
	}
	if f.EndpointGroupID != "" {
		arg.EndpointGroupID = &f.EndpointGroupID
	}
	if f.Trigger != "" {
		arg.TriggerFilter = &f.Trigger
	}
	var cursor eventCursor
	hasCursor, err := apiutil.DecodeCursor(f.PageToken, &cursor)
	if err != nil {
		return EventPage{}, err
	}
	if hasCursor {
		at := cursor.CreatedAt
		arg.CursorAt = &at
		arg.CursorID = cursor.ID
	}

	qctx, cancel := s.ioContext(ctx)
	rows, err := s.q.BreakerListEvents(qctx, arg)
	cancel()
	if err != nil {
		return EventPage{}, apperr.Internal(fmt.Errorf("list breaker events of namespace %s: %w", ns.ID, err))
	}
	size := int(arg.PageLimit) - 1
	page := EventPage{Events: make([]Event, 0, min(len(rows), size))}
	for i, r := range rows {
		if i == size {
			last := rows[size-1]
			if page.NextPageToken, err = apiutil.EncodeCursor(eventCursor{CreatedAt: last.CreatedAt, ID: last.ID}); err != nil {
				return EventPage{}, apperr.Internal(err)
			}
			break
		}
		page.Events = append(page.Events, eventFromRow(r))
	}
	return page, nil
}

func eventFromRow(r breakerdb.BreakerListEventsRow) Event {
	ev := Event{
		ID:              r.ID,
		CreatedAt:       r.CreatedAt.UTC(),
		SiteID:          r.SiteID,
		Site:            r.SiteName,
		Client:          r.Client,
		EndpointGroup:   r.EndpointGroupName,
		EndpointGroupID: r.EndpointGroupID,
		FromState:       r.FromState,
		ToState:         r.ToState,
		Trigger:         r.Trigger,
		Reason:          r.Reason,
		OpenUntil:       r.OpenUntil,
		Actor:           r.Actor,
		Metrics:         map[string]any{},
	}
	if len(r.Metrics) > 0 {
		// A malformed metrics document is shown as empty rather than failing the page.
		_ = json.Unmarshal(r.Metrics, &ev.Metrics)
		if ev.Metrics == nil {
			ev.Metrics = map[string]any{}
		}
	}
	return ev
}
