package analytics

import (
	"context"
	"fmt"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/apperr"
)

// MaxNodes caps the number of nodes returned by NodeStats (busiest first).
const MaxNodes = 1000

// NodeStat aggregates the lease and report counters of one node.
type NodeStat struct {
	// Node is the X-Spinneret-Node name ("_" when absent).
	Node      string
	Acquires  int64
	Reports   int64
	Abandoned int64
	Rejected  int64
	// UnreportedRatio is Abandoned / Acquires (0 without acquires).
	UnreportedRatio float64
}

// NodeStats returns per-node counters of a namespace over the range (default
// last hour, at most 31 days; the start is aligned down to its minute),
// ordered by acquires descending then node name. Node statistics are not
// site-scoped: callers must require namespace-wide dashboard access.
func (s *Service) NodeStats(ctx context.Context, namespaceID string, r TimeRange) ([]NodeStat, error) {
	if namespaceID == "" {
		return nil, apperr.InvalidArgument("", "namespace is required")
	}
	start, end, err := r.resolve(s.now(), DefaultNodeStatsRange, MaxAggregateRange)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	rows, err := s.q.AnalyticsNodeStats(ctx, analyticsdb.AnalyticsNodeStatsParams{
		NamespaceID: namespaceID,
		FromBucket:  minuteFloor(start),
		ToBucket:    end,
		MaxRows:     MaxNodes,
	})
	if err != nil {
		return nil, fmt.Errorf("sum node statistics: %w", err)
	}
	out := make([]NodeStat, 0, len(rows))
	for _, row := range rows {
		out = append(out, NodeStat{
			Node:            row.Node,
			Acquires:        row.Acquires,
			Reports:         row.Reports,
			Abandoned:       row.Abandoned,
			Rejected:        row.Rejected,
			UnreportedRatio: ratio(row.Abandoned, row.Acquires),
		})
	}
	return out, nil
}
