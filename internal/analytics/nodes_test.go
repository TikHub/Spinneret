package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func (e *env) nodeStat(bucket time.Time, ns, node string, acquires, reports, abandoned, rejected int64) {
	e.t.Helper()
	e.exec(`INSERT INTO node_stats_minutely (bucket, namespace_id, node, acquires, reports, abandoned, rejected)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, bucket, ns, node, acquires, reports, abandoned, rejected)
}

func TestNodeStats(t *testing.T) {
	e := newEnv(t, false)
	cur := minuteFloor(e.now)
	e.nodeStat(cur, namespaceID, "n1", 100, 90, 5, 1)
	e.nodeStat(cur.Add(-30*time.Minute), namespaceID, "n1", 100, 95, 15, 0)
	e.nodeStat(cur.Add(-60*time.Minute), namespaceID, "n1", 1000, 0, 0, 0) // start minute is included (aligned down)
	e.nodeStat(cur.Add(-61*time.Minute), namespaceID, "n1", 5000, 0, 0, 0) // before the range
	e.nodeStat(cur.Add(-5*time.Minute), namespaceID, "_", 0, 0, 0, 7)
	e.nodeStat(cur.Add(-5*time.Minute), namespaceID, "n2", 400, 400, 0, 0)
	e.nodeStat(cur.Add(-5*time.Minute), "ns_other", "n3", 9999, 0, 0, 0)

	nodes, err := e.svc.NodeStats(e.ctx, namespaceID, TimeRange{})
	require.NoError(t, err)
	require.Equal(t, []NodeStat{
		{Node: "n1", Acquires: 1200, Reports: 185, Abandoned: 20, Rejected: 1, UnreportedRatio: 20.0 / 1200},
		{Node: "n2", Acquires: 400, Reports: 400},
		{Node: "_", Rejected: 7},
	}, nodes)

	nodes, err = e.svc.NodeStats(e.ctx, namespaceID, TimeRange{Start: ptr(cur.Add(-10 * time.Minute)), End: ptr(cur)})
	require.NoError(t, err)
	require.Equal(t, []NodeStat{{Node: "n2", Acquires: 400, Reports: 400}, {Node: "_", Rejected: 7}}, nodes)

	_, err = e.svc.NodeStats(e.ctx, namespaceID, TimeRange{Start: ptr(e.now.Add(-40 * 24 * time.Hour))})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = e.svc.NodeStats(e.ctx, "", TimeRange{})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
}
