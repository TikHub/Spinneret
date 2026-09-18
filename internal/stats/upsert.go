package stats

import (
	"context"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/stats/statsdb"
)

// queries returns the sqlc queries bound to the pool.
func (a *Aggregator) queries() (*statsdb.Queries, error) {
	if a.pool == nil {
		return nil, errNoPool
	}
	return statsdb.New(a.pool), nil
}

// writeAcquire upserts one chunk of acquire_stats_minutely rows.
func (a *Aggregator) writeAcquire(ctx context.Context, rows []entry[acquireKey, acquireValue]) error {
	q, err := a.queries()
	if err != nil {
		return err
	}
	p := statsdb.StatsUpsertAcquireMinutelyParams{
		Buckets:          make([]time.Time, len(rows)),
		NamespaceIds:     make([]string, len(rows)),
		SiteIds:          make([]string, len(rows)),
		EndpointGroupIds: make([]string, len(rows)),
		Results:          make([]string, len(rows)),
		Counts:           make([]int64, len(rows)),
		DurationUsSums:   make([]int64, len(rows)),
	}
	for i, r := range rows {
		p.Buckets[i] = unixTime(r.key.bucket)
		p.NamespaceIds[i] = r.key.namespaceID
		p.SiteIds[i] = r.key.siteID
		p.EndpointGroupIds[i] = r.key.endpointGroupID
		p.Results[i] = r.key.result
		p.Counts[i] = r.val.count
		p.DurationUsSums[i] = r.val.durationUs
	}
	return q.StatsUpsertAcquireMinutely(ctx, p)
}

// writePayload upserts one chunk of payload_access_minutely rows.
func (a *Aggregator) writePayload(ctx context.Context, rows []entry[payloadKey, countValue]) error {
	q, err := a.queries()
	if err != nil {
		return err
	}
	p := statsdb.StatsUpsertPayloadAccessMinutelyParams{
		Buckets:         make([]time.Time, len(rows)),
		NamespaceIds:    make([]string, len(rows)),
		TokenIds:        make([]string, len(rows)),
		IdentityTypeIds: make([]string, len(rows)),
		Counts:          make([]int64, len(rows)),
	}
	for i, r := range rows {
		p.Buckets[i] = unixTime(r.key.bucket)
		p.NamespaceIds[i] = r.key.namespaceID
		p.TokenIds[i] = r.key.tokenID
		p.IdentityTypeIds[i] = r.key.identityTypeID
		p.Counts[i] = r.val.count
	}
	return q.StatsUpsertPayloadAccessMinutely(ctx, p)
}

// writeNode upserts one chunk of node_stats_minutely rows.
func (a *Aggregator) writeNode(ctx context.Context, rows []entry[nodeKey, nodeValue]) error {
	q, err := a.queries()
	if err != nil {
		return err
	}
	p := statsdb.StatsUpsertNodeMinutelyParams{
		Buckets:      make([]time.Time, len(rows)),
		NamespaceIds: make([]string, len(rows)),
		Nodes:        make([]string, len(rows)),
		Acquires:     make([]int64, len(rows)),
		Reports:      make([]int64, len(rows)),
		Abandoned:    make([]int64, len(rows)),
		Rejected:     make([]int64, len(rows)),
	}
	for i, r := range rows {
		p.Buckets[i] = unixTime(r.key.bucket)
		p.NamespaceIds[i] = r.key.namespaceID
		p.Nodes[i] = r.key.node
		p.Acquires[i] = r.val.acquires
		p.Reports[i] = r.val.reports
		p.Abandoned[i] = r.val.abandoned
		p.Rejected[i] = r.val.rejected
	}
	return q.StatsUpsertNodeMinutely(ctx, p)
}

// writeOutcome upserts one chunk of outcome_stats_minutely rows.
func (a *Aggregator) writeOutcome(ctx context.Context, rows []entry[outcomeKey, outcomeValue]) error {
	q, err := a.queries()
	if err != nil {
		return err
	}
	p := statsdb.StatsUpsertOutcomeMinutelyParams{
		Buckets:           make([]time.Time, len(rows)),
		NamespaceIds:      make([]string, len(rows)),
		SiteIds:           make([]string, len(rows)),
		EndpointGroupIds:  make([]string, len(rows)),
		ProxyIds:          make([]string, len(rows)),
		Outcomes:          make([]string, len(rows)),
		Counts:            make([]int64, len(rows)),
		LatencyMsSums:     make([]int64, len(rows)),
		ResponseBytesSums: make([]int64, len(rows)),
	}
	for i, r := range rows {
		p.Buckets[i] = unixTime(r.key.bucket)
		p.NamespaceIds[i] = r.key.namespaceID
		p.SiteIds[i] = r.key.siteID
		p.EndpointGroupIds[i] = r.key.endpointGroupID
		p.ProxyIds[i] = r.key.proxyID
		p.Outcomes[i] = r.key.outcome
		p.Counts[i] = r.val.count
		p.LatencyMsSums[i] = r.val.latencyMs
		p.ResponseBytesSums[i] = r.val.responseBytes
	}
	return q.StatsUpsertOutcomeMinutely(ctx, p)
}

// writeIdentity upserts one chunk of identity_stats_hourly rows.
func (a *Aggregator) writeIdentity(ctx context.Context, rows []entry[identityKey, identityValue]) error {
	q, err := a.queries()
	if err != nil {
		return err
	}
	p := statsdb.StatsUpsertIdentityHourlyParams{
		Buckets:          make([]time.Time, len(rows)),
		SiteIds:          make([]string, len(rows)),
		IdentityIds:      make([]string, len(rows)),
		EndpointGroupIds: make([]string, len(rows)),
		Outcomes:         make([]string, len(rows)),
		Counts:           make([]int64, len(rows)),
	}
	for i, r := range rows {
		p.Buckets[i] = unixTime(r.key.bucket)
		p.SiteIds[i] = r.val.siteID
		p.IdentityIds[i] = r.key.identityID
		p.EndpointGroupIds[i] = r.key.endpointGroupID
		p.Outcomes[i] = r.key.outcome
		p.Counts[i] = r.val.count
	}
	return q.StatsUpsertIdentityHourly(ctx, p)
}
