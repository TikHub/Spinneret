package breaker

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// readsPerPipeline bounds the breaker reads sent in one pipeline.
const readsPerPipeline = 500

// Status is the live breaker state of one endpoint group.
type Status struct {
	TenantID        string
	NamespaceID     string
	Namespace       string
	SiteID          string
	Site            string
	Client          string
	EndpointGroup   string
	EndpointGroupID string

	// State is closed, open or half_open.
	State string
	// OpenUntil is the end of a timed open period; zero when not open or
	// opened indefinitely.
	OpenUntil        time.Time
	ConsecutiveOpens int64
	Manual           bool
	Reason           string
	Version          int64
	LastOpenedAt     time.Time
	LastClosedAt     time.Time

	Window WindowMetrics
	Probe  ProbeMetrics

	SitePaused bool
	PolicyID   string
	PolicyName string
}

// WindowMetrics are the sliding window sums used for trip evaluation.
type WindowMetrics struct {
	Total             int64
	Success           int64
	Risk              int64
	CaptchaIdentities int64
	RiskRatio         float64
	SuccessRatio      float64
}

// ProbeMetrics are the half-open probe statistics.
type ProbeMetrics struct {
	Samples   int64
	Successes int64
	// Issued counts probe leases issued in the current 10 s probe window.
	Issued int64
}

// ListFilter selects breakers of a namespace.
type ListFilter struct {
	// Site restricts the result to one site name ("" = all accessible sites).
	Site string
	// Client restricts the result to one client type.
	Client string
	// States restricts the result to the given states (empty = all).
	States []string
	// PageSize is normalized with apiutil.PageSize.
	PageSize int32
	// PageToken continues a previous page.
	PageToken string
}

// ListPage is one page of breakers.
type ListPage struct {
	Breakers      []Status
	NextPageToken string
	Total         int
}

// listCursor is the keyset position of ListBreakers pages.
type listCursor struct {
	Severity int    `json:"s"`
	Site     string `json:"n"`
	Client   string `json:"c"`
	Group    string `json:"g"`
	ID       string `json:"i"`
}

// Get returns the breaker status of one endpoint group (breaker:read on its site).
func (s *Service) Get(ctx context.Context, p *authz.Principal, groupID string) (*Status, error) {
	ns, site, g, err := s.resolveGroup(p, groupID)
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermBreakerRead, siteResource(ns, site)); err != nil {
		return nil, err
	}
	statuses, err := s.readStatuses(ctx, []candidate{{ns: ns, site: site, group: g}})
	if err != nil {
		return nil, err
	}
	return &statuses[0], nil
}

// List returns the breakers of the namespace's endpoint groups the principal
// can read, ordered by state severity (open, half_open, closed), site name,
// client and group name.
func (s *Service) List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f ListFilter) (ListPage, error) {
	if p == nil {
		return ListPage{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	var sites []*catalog.Site
	if f.Site != "" {
		site, ok := ns.Sites[f.Site]
		if !ok {
			return ListPage{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", f.Site)
		}
		if err := p.Require(authz.PermBreakerRead, siteResource(ns, site)); err != nil {
			return ListPage{}, err
		}
		sites = []*catalog.Site{site}
	} else {
		_, accessible := accessibleSites(p, ns, authz.PermBreakerRead)
		if len(accessible) == 0 {
			if err := p.Require(authz.PermBreakerRead, namespaceResource(ns)); err != nil {
				return ListPage{}, err
			}
		}
		for _, site := range accessible {
			sites = append(sites, site)
		}
	}
	var cursor listCursor
	hasCursor, err := apiutil.DecodeCursor(f.PageToken, &cursor)
	if err != nil {
		return ListPage{}, err
	}

	var groups []candidate
	for _, site := range sites {
		for _, g := range site.GroupsByID {
			if f.Client == "" || g.Client == f.Client {
				groups = append(groups, candidate{ns: ns, site: site, group: g})
			}
		}
	}
	states, err := s.readStates(ctx, groups)
	if err != nil {
		return ListPage{}, err
	}
	type row struct {
		key listCursor
		c   candidate
	}
	rows := make([]row, 0, len(groups))
	for i, c := range groups {
		if len(f.States) > 0 && !slices.Contains(f.States, states[i]) {
			continue
		}
		rows = append(rows, row{
			key: listCursor{Severity: severity(states[i]), Site: c.site.Name, Client: c.group.Client, Group: c.group.Name, ID: c.group.ID},
			c:   c,
		})
	}
	slices.SortFunc(rows, func(a, b row) int { return compareCursor(a.key, b.key) })
	page := ListPage{Total: len(rows)}
	start := 0
	if hasCursor {
		start, _ = slices.BinarySearchFunc(rows, cursor, func(r row, c listCursor) int {
			if compareCursor(r.key, c) <= 0 {
				return -1
			}
			return 1
		})
	}
	size := apiutil.PageSize(f.PageSize)
	end := min(start+size, len(rows))
	selected := make([]candidate, 0, end-start)
	for _, r := range rows[start:end] {
		selected = append(selected, r.c)
	}
	if page.Breakers, err = s.readStatuses(ctx, selected); err != nil {
		return ListPage{}, err
	}
	if end < len(rows) && end > start {
		if page.NextPageToken, err = apiutil.EncodeCursor(rows[end-1].key); err != nil {
			return ListPage{}, apperr.Internal(err)
		}
	}
	return page, nil
}

func severity(state string) int {
	switch state {
	case StateOpen:
		return 0
	case StateHalfOpen:
		return 1
	default:
		return 2
	}
}

func compareCursor(a, b listCursor) int {
	return cmp.Or(
		cmp.Compare(a.Severity, b.Severity),
		cmp.Compare(a.Site, b.Site),
		cmp.Compare(a.Client, b.Client),
		cmp.Compare(a.Group, b.Group),
		cmp.Compare(a.ID, b.ID),
	)
}

// readStates returns the breaker state of each group (pipelined HGET).
func (s *Service) readStates(ctx context.Context, groups []candidate) ([]string, error) {
	out := make([]string, len(groups))
	for start := 0; start < len(groups); start += readsPerPipeline {
		end := min(start+readsPerPipeline, len(groups))
		cmds := make(rueidis.Commands, 0, end-start)
		for _, c := range groups[start:end] {
			cmds = append(cmds, s.rdb.B().Hget().Key(s.keys.Breaker(c.site.Key, c.group.Key)).Field("st").Build())
		}
		rctx, cancel := s.ioContext(ctx)
		results := s.rdb.DoMulti(rctx, cmds...)
		cancel()
		for i, res := range results {
			st, err := res.ToString()
			if err != nil && !rueidis.IsRedisNil(err) {
				return nil, apperr.Internal(fmt.Errorf("read breaker state: %w", err))
			}
			out[start+i] = normalizeState(st)
		}
	}
	return out, nil
}

// readStatuses reads the full status (state, window and probe metrics) of each
// group with breaker_eval.lua in read mode.
func (s *Service) readStatuses(ctx context.Context, groups []candidate) ([]Status, error) {
	out := make([]Status, 0, len(groups))
	now := s.now()
	for start := 0; start < len(groups); start += readsPerPipeline {
		end := min(start+readsPerPipeline, len(groups))
		calls := make([]rueidis.LuaExec, 0, end-start)
		for _, c := range groups[start:end] {
			calls = append(calls, evalExec(s.keys, c.site.Key, c.group.Key, now, paramsFor(c.group.Breaker), modeRead))
		}
		rctx, cancel := s.ioContext(ctx)
		results := evalScript.ExecMulti(rctx, s.rdb, calls...)
		cancel()
		for i, res := range results {
			r, err := parseEvalResult(res)
			if err != nil {
				return nil, apperr.Internal(fmt.Errorf("read breaker status: %w", err))
			}
			out = append(out, buildStatus(groups[start+i], r.Hash, r.Window, now))
		}
	}
	return out, nil
}

// buildStatus assembles a Status from the hot state of a group.
func buildStatus(c candidate, h Hash, w Window, now time.Time) Status {
	st := Status{
		TenantID:         c.ns.TenantID,
		NamespaceID:      c.ns.ID,
		Namespace:        c.ns.Name,
		SiteID:           c.site.ID,
		Site:             c.site.Name,
		Client:           c.group.Client,
		EndpointGroup:    c.group.Name,
		EndpointGroupID:  c.group.ID,
		State:            h.State,
		ConsecutiveOpens: h.OpenCount,
		Manual:           h.Manual,
		Reason:           h.Reason,
		Version:          h.Version,
		LastOpenedAt:     millisTime(h.LastOpenedMs),
		LastClosedAt:     millisTime(h.LastClosedMs),
		Window: WindowMetrics{
			Total:             w.Total,
			Success:           w.Success,
			Risk:              w.Risk,
			CaptchaIdentities: w.CaptchaIdentities,
			RiskRatio:         roundRatio(w.RiskRatio()),
			SuccessRatio:      roundRatio(w.SuccessRatio()),
		},
		Probe: ProbeMetrics{
			Samples:   h.ProbeSamples,
			Successes: h.ProbeSuccesses,
		},
		SitePaused: c.site.Paused,
		PolicyID:   c.group.BreakerRef.PolicyID,
		PolicyName: c.group.BreakerRef.Name,
	}
	if h.State == StateOpen && h.OpenUntilMs > 0 {
		st.OpenUntil = millisTime(h.OpenUntilMs)
	}
	if h.State == StateHalfOpen && h.HalfOpenMs > 0 && now.Sub(millisTime(h.HalfOpenMs)) < probeWindow {
		st.Probe.Issued = h.ProbesIssued
	}
	return st
}

// millisTime converts Unix milliseconds to UTC time; values <= 0 yield zero.
func millisTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
