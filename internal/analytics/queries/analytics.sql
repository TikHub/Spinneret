-- Dashboard read queries (analytics track). Every query is read-only.

-- AnalyticsIdentityStateCounts counts identities per site and lifecycle state.
-- name: AnalyticsIdentityStateCounts :many
SELECT site_id, state, count(*)::bigint AS count
FROM identities
WHERE site_id = ANY(sqlc.arg(site_ids)::text[])
GROUP BY site_id, state;

-- AnalyticsProxyStateCounts counts the proxies of a namespace per state.
-- name: AnalyticsProxyStateCounts :many
SELECT state, count(*)::bigint AS count
FROM proxies
WHERE namespace_id = sqlc.arg(namespace_id)
GROUP BY state;

-- AnalyticsAcquireTotalsBySite sums acquire results per site over a bucket range.
-- name: AnalyticsAcquireTotalsBySite :many
SELECT site_id, result, sum(count)::bigint AS count
FROM acquire_stats_minutely
WHERE namespace_id = sqlc.arg(namespace_id)
  AND bucket >= sqlc.arg(from_bucket)
  AND bucket < sqlc.arg(to_bucket)
  AND site_id = ANY(sqlc.arg(site_ids)::text[])
GROUP BY site_id, result;

-- AnalyticsOutcomeTotalsBySite sums report outcomes per site over a bucket range.
-- name: AnalyticsOutcomeTotalsBySite :many
SELECT site_id, outcome, sum(count)::bigint AS count
FROM outcome_stats_minutely
WHERE namespace_id = sqlc.arg(namespace_id)
  AND bucket >= sqlc.arg(from_bucket)
  AND bucket < sqlc.arg(to_bucket)
  AND site_id = ANY(sqlc.arg(site_ids)::text[])
GROUP BY site_id, outcome;

-- AnalyticsAcquireSeries buckets acquire results by a fixed step aligned to
-- the Unix epoch. all_sites disables the site filter; an empty
-- endpoint_group_ids array disables the endpoint group filter.
-- name: AnalyticsAcquireSeries :many
SELECT date_bin(make_interval(secs => sqlc.arg(step_seconds)::double precision), bucket, 'epoch'::timestamptz)::timestamptz AS ts,
       result,
       sum(count)::bigint AS count
FROM acquire_stats_minutely
WHERE namespace_id = sqlc.arg(namespace_id)
  AND bucket >= sqlc.arg(from_bucket)
  AND bucket < sqlc.arg(to_bucket)
  AND (sqlc.arg(all_sites)::boolean OR site_id = ANY(sqlc.arg(site_ids)::text[]))
  AND (cardinality(sqlc.arg(endpoint_group_ids)::text[]) = 0 OR endpoint_group_id = ANY(sqlc.arg(endpoint_group_ids)::text[]))
GROUP BY 1, 2
ORDER BY 1, 2;

-- AnalyticsOutcomeSeries buckets report outcomes (count and latency sum) by a
-- fixed step aligned to the Unix epoch, with the filters of AnalyticsAcquireSeries.
-- name: AnalyticsOutcomeSeries :many
SELECT date_bin(make_interval(secs => sqlc.arg(step_seconds)::double precision), bucket, 'epoch'::timestamptz)::timestamptz AS ts,
       outcome,
       sum(count)::bigint AS count,
       sum(latency_ms_sum)::bigint AS latency_ms_sum
FROM outcome_stats_minutely
WHERE namespace_id = sqlc.arg(namespace_id)
  AND bucket >= sqlc.arg(from_bucket)
  AND bucket < sqlc.arg(to_bucket)
  AND (sqlc.arg(all_sites)::boolean OR site_id = ANY(sqlc.arg(site_ids)::text[]))
  AND (cardinality(sqlc.arg(endpoint_group_ids)::text[]) = 0 OR endpoint_group_id = ANY(sqlc.arg(endpoint_group_ids)::text[]))
GROUP BY 1, 2
ORDER BY 1, 2;

-- AnalyticsNodeStats sums node counters over a bucket range, busiest nodes first.
-- name: AnalyticsNodeStats :many
SELECT node,
       sum(acquires)::bigint AS acquires,
       sum(reports)::bigint AS reports,
       sum(abandoned)::bigint AS abandoned,
       sum(rejected)::bigint AS rejected
FROM node_stats_minutely
WHERE namespace_id = sqlc.arg(namespace_id)
  AND bucket >= sqlc.arg(from_bucket)
  AND bucket < sqlc.arg(to_bucket)
GROUP BY node
ORDER BY acquires DESC, node
LIMIT sqlc.arg(max_rows);

-- AnalyticsHeatmapIdentities pages the identities of a site client (keyset on id).
-- name: AnalyticsHeatmapIdentities :many
SELECT i.id,
       i.hkey,
       i.state,
       i.region,
       i.ban_until,
       COALESCE(a.external_ref, '')::text AS account_ref,
       COALESCE(a.hkey, 0)::bigint AS account_hkey
FROM identities i
LEFT JOIN accounts a ON a.id = i.account_id
WHERE i.site_id = sqlc.arg(site_id)
  AND i.client = sqlc.arg(client)
  AND i.state = ANY(sqlc.arg(states)::text[])
  AND i.id > sqlc.arg(after_id)
ORDER BY i.id
LIMIT sqlc.arg(max_rows);

-- AnalyticsHeatmapIdentityCount counts the identities matched by AnalyticsHeatmapIdentities.
-- name: AnalyticsHeatmapIdentityCount :one
SELECT count(*)::bigint AS count
FROM identities
WHERE site_id = sqlc.arg(site_id)
  AND client = sqlc.arg(client)
  AND state = ANY(sqlc.arg(states)::text[]);

-- AnalyticsRiskEvents pages risk events newest first over a set of sites. The
-- page of every site is read through the (namespace_id, site_id, created_at)
-- index and the pages are merged. The keyset cursor is (before_at, before_id):
-- rows strictly before it in (created_at DESC, id DESC) order are returned;
-- without a cursor before_at is the exclusive range end and before_id is the
-- empty string. Empty string filters are disabled.
-- name: AnalyticsRiskEvents :many
SELECT e.id,
       e.created_at,
       e.site_id,
       e.endpoint_group_id,
       e.identity_id,
       e.proxy_id,
       e.lease_id,
       e.report_id,
       e.node,
       e.token_id,
       e.uri,
       e.method,
       e.http_status,
       e.business_code,
       e.error_kind,
       e.markers,
       e.outcome,
       e.blame,
       e.rule,
       e.latency_ms,
       e.response_bytes,
       e.started_at,
       e.finished_at
FROM unnest(sqlc.arg(site_ids)::text[]) AS s (site_id)
CROSS JOIN LATERAL (
    SELECT r.id, r.created_at, r.site_id, r.endpoint_group_id, r.identity_id, r.proxy_id, r.lease_id, r.report_id,
           r.node, r.token_id, r.uri, r.method, r.http_status, r.business_code, r.error_kind, r.markers, r.outcome,
           r.blame, r.rule, r.latency_ms, r.response_bytes, r.started_at, r.finished_at
    FROM risk_events r
    WHERE r.namespace_id = sqlc.arg(namespace_id)
      AND r.site_id = s.site_id
      AND r.created_at >= sqlc.arg(from_at)
      AND r.created_at <= sqlc.arg(before_at)
      AND (r.created_at < sqlc.arg(before_at) OR r.id < sqlc.arg(before_id)::text)
      AND (sqlc.arg(endpoint_group_id)::text = '' OR r.endpoint_group_id = sqlc.arg(endpoint_group_id)::text)
      AND (sqlc.arg(outcome)::text = '' OR r.outcome = sqlc.arg(outcome)::text)
      AND (sqlc.arg(identity_id)::text = '' OR r.identity_id = sqlc.arg(identity_id)::text)
      AND (sqlc.arg(proxy_id)::text = '' OR r.proxy_id = sqlc.arg(proxy_id)::text)
      AND (sqlc.arg(node)::text = '' OR r.node = sqlc.arg(node)::text)
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT sqlc.arg(max_rows)
) e
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows);
