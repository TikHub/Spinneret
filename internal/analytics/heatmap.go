package analytics

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// Heatmap limits.
const (
	DefaultHeatmapLimit = 100
	MaxHeatmapLimit     = 500
	// labelSuffixLen is the number of trailing ID characters used as a row label.
	labelSuffixLen = 8
)

// identityStates lists every identity lifecycle state.
var identityStates = []string{"pending", "active", "expired", "banned", "quarantined", "disabled", "retired"}

// defaultHeatmapStates are the states shown when the query names none.
var defaultHeatmapStates = []string{"pending", "active", "expired", "banned", "quarantined", "disabled"}

// HeatmapQuery selects the identities of a site client.
type HeatmapQuery struct {
	SiteID string
	Client string
	// States filters identities; empty means every state except retired.
	States []string
	// Limit is the maximum number of rows (0 = DefaultHeatmapLimit, at most MaxHeatmapLimit).
	Limit int
	// AfterID continues after the last identity of the previous page.
	AfterID string
}

// HeatmapColumn is an endpoint group of the heatmap.
type HeatmapColumn struct {
	EndpointGroupID string
	EndpointGroup   string
}

// HeatmapRow is an identity of the heatmap.
type HeatmapRow struct {
	IdentityID string
	// Label is the account reference, the region or the ID suffix.
	Label string
	State string
}

// HeatmapCell is the hot state of one identity in one endpoint group.
type HeatmapCell struct {
	Row int
	Col int
	// Score is the decayed health score.
	Score float64
	// CooldownRemainingMs is the remaining endpoint, site or account cooldown
	// (or ban: math.MaxInt64 for permanent bans).
	CooldownRemainingMs int64
	// Available reports whether the identity can be leased in the group now.
	Available bool
}

// Heatmap is a sparse identity x endpoint group matrix. A cell is omitted when
// the identity has no health entry in the group, no cooldown or ban, and its
// availability equals what its state implies (pending and active identities
// available, others not); omitted cells have the group's baseline score.
type Heatmap struct {
	Columns []HeatmapColumn
	Rows    []HeatmapRow
	Cells   []HeatmapCell
	// NextAfterID is the cursor of the next page ("" when there is none).
	NextAfterID string
	// Total is the number of identities matching the query.
	Total       int64
	GeneratedAt time.Time
}

// identityHot is the identity-wide hot state relevant to cells.
type identityHot struct {
	cooldownUntil int64
	banned        bool
	banPermanent  bool
	banUntil      int64
}

// Heatmap returns the identity x endpoint group matrix of a readable site client.
func (s *Service) Heatmap(ctx context.Context, scope Scope, q HeatmapQuery) (Heatmap, error) {
	rs, err := s.resolve(scope)
	if err != nil {
		return Heatmap{}, err
	}
	site, err := rs.site(q.SiteID)
	if err != nil {
		return Heatmap{}, err
	}
	if !site.HasClient(q.Client) {
		return Heatmap{}, apperr.InvalidArgument(apperr.ReasonClientUnknown, "site %q has no client %q", site.Name, q.Client)
	}
	states, err := heatmapStates(q.States)
	if err != nil {
		return Heatmap{}, err
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultHeatmapLimit
	case limit > MaxHeatmapLimit:
		limit = MaxHeatmapLimit
	}
	groups := site.GroupsOfClient(q.Client)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	identities, err := s.q.AnalyticsHeatmapIdentities(ctx, analyticsdb.AnalyticsHeatmapIdentitiesParams{
		SiteID: site.ID, Client: q.Client, States: states, AfterID: q.AfterID, MaxRows: int32(limit + 1),
	})
	if err != nil {
		return Heatmap{}, fmt.Errorf("list heatmap identities: %w", err)
	}
	total, err := s.q.AnalyticsHeatmapIdentityCount(ctx, analyticsdb.AnalyticsHeatmapIdentityCountParams{
		SiteID: site.ID, Client: q.Client, States: states,
	})
	if err != nil {
		return Heatmap{}, fmt.Errorf("count heatmap identities: %w", err)
	}
	now := s.now().UTC()
	out := Heatmap{
		Columns:     make([]HeatmapColumn, len(groups)),
		Rows:        make([]HeatmapRow, 0, min(len(identities), limit)),
		Cells:       []HeatmapCell{},
		Total:       total,
		GeneratedAt: now,
	}
	for i, g := range groups {
		out.Columns[i] = HeatmapColumn{EndpointGroupID: g.ID, EndpointGroup: g.Name}
	}
	if len(identities) > limit {
		identities = identities[:limit]
		out.NextAfterID = identities[limit-1].ID
	}
	for _, id := range identities {
		out.Rows = append(out.Rows, HeatmapRow{IdentityID: id.ID, Label: heatmapLabel(id), State: id.State})
	}
	if len(identities) == 0 {
		return out, nil
	}
	cells, err := s.heatmapCells(ctx, site, groups, identities, now)
	if err != nil {
		return Heatmap{}, err
	}
	out.Cells = cells
	return out, nil
}

// heatmapStates validates the state filter.
func heatmapStates(states []string) ([]string, error) {
	if len(states) == 0 {
		return slices.Clone(defaultHeatmapStates), nil
	}
	for _, st := range states {
		if !slices.Contains(identityStates, st) {
			return nil, apperr.InvalidArgument("", "unknown identity state %q", st)
		}
	}
	return slices.Clone(states), nil
}

// heatmapLabel picks the short row label of an identity.
func heatmapLabel(r analyticsdb.AnalyticsHeatmapIdentitiesRow) string {
	switch {
	case r.AccountRef != "":
		return r.AccountRef
	case r.Region != "":
		return r.Region
	case len(r.ID) > labelSuffixLen:
		return r.ID[len(r.ID)-labelSuffixLen:]
	default:
		return r.ID
	}
}

// heatmapCells reads the hot state of the identities in one pipelined round
// trip and builds the non-default cells.
func (s *Service) heatmapCells(ctx context.Context, site *catalog.Site, groups []*catalog.EndpointGroup,
	identities []analyticsdb.AnalyticsHeatmapIdentitiesRow, now time.Time) ([]HeatmapCell, error) {
	hot, err := s.readHeatmapHot(ctx, site, groups, identities)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	cells := []HeatmapCell{}
	for col, g := range groups {
		health := healthOf(g)
		for row, id := range identities {
			packed, hasHealth := hot.health[col][row], hot.hasHealth[col][row]
			score := health.Baseline
			var cooldown int64
			if hasHealth {
				e := parseHealth(packed, health.Baseline, nowMs)
				score = decayScore(e.score, e.scoreTS, nowMs, health.Baseline, health.Tau.Std())
				cooldown = e.cooldown
			}
			ih := hot.identities[row]
			remaining := max(max(cooldown, ih.cooldownUntil)-nowMs, 0)
			if ih.banned {
				if ih.banPermanent {
					remaining = math.MaxInt64
				} else {
					remaining = max(remaining, ih.banUntil-nowMs)
				}
			}
			available := hot.inReady[col][row] && hot.readyAt[col][row] <= float64(nowMs)
			if !hasHealth && remaining == 0 && available == leasableState(id.State) {
				continue
			}
			cells = append(cells, HeatmapCell{
				Row: row, Col: col, Score: score, CooldownRemainingMs: remaining, Available: available,
			})
		}
	}
	return cells, nil
}

// leasableState reports whether identities in state are normally in the ready queues.
func leasableState(state string) bool {
	return state == "active" || state == "pending"
}
