package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/policy"
)

// Page size limits of event listings.
const (
	DefaultEventPageSize = 50
	MaxEventPageSize     = 500
)

// RiskEventQuery filters and pages risk events. Empty filters match everything.
type RiskEventQuery struct {
	SiteID          string
	EndpointGroupID string
	Outcome         string
	IdentityID      string
	ProxyID         string
	Node            string
	// Range defaults to the last 24 hours and may span at most 31 days.
	Range TimeRange
	// PageSize is the maximum number of events (0 = default 50, at most 500).
	PageSize int
	// Cursor continues after the last event of a previous page.
	Cursor *RiskCursor
}

// RiskCursor is the keyset position of a risk event page.
type RiskCursor struct {
	// CreatedAtUs is created_at of the last returned event in Unix microseconds.
	CreatedAtUs int64 `json:"t"`
	// ID is the ID of the last returned event.
	ID string `json:"i"`
}

// RiskEvent is a report with a non-success outcome.
type RiskEvent struct {
	ID              string
	CreatedAt       time.Time
	SiteID          string
	Site            string
	Client          string
	EndpointGroupID string
	EndpointGroup   string
	IdentityID      string
	ProxyID         string
	LeaseID         string
	ReportID        string
	Node            string
	TokenID         string
	URI             string
	Method          string
	HTTPStatus      int32
	BusinessCode    string
	ErrorKind       string
	Markers         []string
	Outcome         string
	Blame           string
	Rule            string
	LatencyMs       int32
	ResponseBytes   int64
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// RiskEventPage is one page of risk events, newest first.
type RiskEventPage struct {
	Events []RiskEvent
	// Next is the cursor of the next page; nil when there are no more events.
	Next *RiskCursor
}

// RiskEvents lists the risk events of the readable sites, newest first.
func (s *Service) RiskEvents(ctx context.Context, scope Scope, q RiskEventQuery) (RiskEventPage, error) {
	rs, err := s.resolve(scope)
	if err != nil {
		return RiskEventPage{}, err
	}
	if q.Outcome != "" && (!policy.ValidOutcome(q.Outcome) || q.Outcome == policy.OutcomeSuccess) {
		return RiskEventPage{}, apperr.InvalidArgument("", "unsupported outcome %q", q.Outcome)
	}
	siteIDs := rs.siteIDs()
	if q.SiteID != "" {
		site, err := rs.site(q.SiteID)
		if err != nil {
			return RiskEventPage{}, err
		}
		siteIDs = []string{site.ID}
	}
	if q.EndpointGroupID != "" {
		_, site, err := rs.group(q.EndpointGroupID)
		if err != nil {
			return RiskEventPage{}, err
		}
		if q.SiteID != "" && site.ID != q.SiteID {
			return RiskEventPage{}, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group does not belong to the site")
		}
		siteIDs = []string{site.ID}
	}
	start, end, err := q.Range.resolve(s.now(), DefaultRiskEventRange, MaxAggregateRange)
	if err != nil {
		return RiskEventPage{}, err
	}
	pageSize := normalizePageSize(q.PageSize)
	beforeAt, beforeID := end, ""
	if q.Cursor != nil {
		if q.Cursor.ID == "" {
			return RiskEventPage{}, apperr.InvalidArgument("", "invalid page_token")
		}
		at := time.UnixMicro(q.Cursor.CreatedAtUs).UTC()
		if at.Before(beforeAt) {
			beforeAt, beforeID = at, q.Cursor.ID
		}
	}
	if len(siteIDs) == 0 || (!beforeAt.After(start) && beforeID == "") {
		return RiskEventPage{Events: []RiskEvent{}}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	rows, err := s.q.AnalyticsRiskEvents(ctx, analyticsdb.AnalyticsRiskEventsParams{
		SiteIds:         siteIDs,
		NamespaceID:     rs.ns.ID,
		FromAt:          start,
		BeforeAt:        beforeAt,
		BeforeID:        beforeID,
		EndpointGroupID: q.EndpointGroupID,
		Outcome:         q.Outcome,
		IdentityID:      q.IdentityID,
		ProxyID:         q.ProxyID,
		Node:            q.Node,
		MaxRows:         int32(pageSize + 1),
	})
	if err != nil {
		return RiskEventPage{}, fmt.Errorf("list risk events: %w", err)
	}
	page := RiskEventPage{Events: make([]RiskEvent, 0, min(len(rows), pageSize))}
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[pageSize-1]
		page.Next = &RiskCursor{CreatedAtUs: last.CreatedAt.UnixMicro(), ID: last.ID}
	}
	for _, r := range rows {
		page.Events = append(page.Events, rs.riskEvent(r))
	}
	return page, nil
}

// riskEvent converts a row, resolving site and endpoint group names from the snapshot.
func (rs *resolvedScope) riskEvent(r analyticsdb.AnalyticsRiskEventsRow) RiskEvent {
	ev := RiskEvent{
		ID:              r.ID,
		CreatedAt:       r.CreatedAt.UTC(),
		SiteID:          r.SiteID,
		EndpointGroupID: r.EndpointGroupID,
		IdentityID:      r.IdentityID,
		ProxyID:         r.ProxyID,
		LeaseID:         r.LeaseID,
		ReportID:        r.ReportID,
		Node:            r.Node,
		TokenID:         r.TokenID,
		URI:             r.Uri,
		Method:          r.Method,
		HTTPStatus:      r.HttpStatus,
		BusinessCode:    r.BusinessCode,
		ErrorKind:       r.ErrorKind,
		Markers:         r.Markers,
		Outcome:         r.Outcome,
		Blame:           r.Blame,
		Rule:            r.Rule,
		LatencyMs:       r.LatencyMs,
		ResponseBytes:   r.ResponseBytes,
		StartedAt:       r.StartedAt,
		FinishedAt:      r.FinishedAt,
	}
	if site, ok := rs.ns.SitesByID[r.SiteID]; ok {
		ev.Site = site.Name
		if g, ok := site.GroupsByID[r.EndpointGroupID]; ok {
			ev.EndpointGroup = g.Name
			ev.Client = g.Client
		}
	}
	return ev
}

// normalizePageSize applies the default and maximum page size.
func normalizePageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultEventPageSize
	case n > MaxEventPageSize:
		return MaxEventPageSize
	default:
		return n
	}
}
