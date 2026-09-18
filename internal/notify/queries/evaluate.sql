-- NotifyProxyStateCounts counts proxies per namespace: all non-retired
-- proxies, active ones and dead ones.
-- name: NotifyProxyStateCounts :many
SELECT n.tenant_id,
       p.namespace_id,
       count(*) FILTER (WHERE p.state <> 'retired')::bigint AS total,
       count(*) FILTER (WHERE p.state = 'active')::bigint AS active,
       count(*) FILTER (WHERE p.state = 'dead')::bigint AS dead
FROM proxies p
JOIN namespaces n ON n.id = p.namespace_id
GROUP BY n.tenant_id, p.namespace_id
ORDER BY p.namespace_id;

-- NotifyBanCounts counts enforced ban state events per site in
-- [from_at, to_at) for the listed namespaces (served by the
-- (namespace_id, created_at) index).
-- name: NotifyBanCounts :many
SELECT tenant_id, namespace_id, site_id, count(*)::bigint AS bans
FROM state_events
WHERE namespace_id = ANY(sqlc.arg(namespace_ids)::text[])
  AND created_at >= sqlc.arg(from_at)
  AND created_at < sqlc.arg(to_at)
  AND action = 'ban'
  AND NOT shadow
  AND site_id <> ''
GROUP BY tenant_id, namespace_id, site_id;

-- NotifyOutcomeTotals sums per-site outcome counts from from_at on.
-- name: NotifyOutcomeTotals :many
SELECT namespace_id,
       site_id,
       sum(count)::bigint AS total,
       (coalesce(sum(count) FILTER (WHERE outcome = 'unknown'), 0))::bigint AS unknown,
       (coalesce(sum(count) FILTER (WHERE outcome = 'client_error'), 0))::bigint AS client_error
FROM outcome_stats_minutely
WHERE bucket >= sqlc.arg(from_at)
GROUP BY namespace_id, site_id
ORDER BY namespace_id, site_id;

-- NotifySecretsExpiring lists secrets whose expiry lies in [from_at, to_at].
-- name: NotifySecretsExpiring :many
SELECT s.id, s.namespace_id, n.tenant_id, s.path, s.expires_at::timestamptz AS expires_at
FROM secrets s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.expires_at IS NOT NULL
  AND s.expires_at >= sqlc.arg(from_at)::timestamptz
  AND s.expires_at <= sqlc.arg(to_at)::timestamptz
ORDER BY s.expires_at, s.id
LIMIT sqlc.arg(limit_rows);

-- name: NotifyIdentityRef :one
SELECT id, site_id, client, type_id FROM identities WHERE id = sqlc.arg(id);

-- name: NotifyTenantName :one
SELECT name FROM tenants WHERE id = sqlc.arg(id);
