-- Queries of the hot-state synchronization layer (internal/hotstate). They
-- load the PostgreSQL truth materialized into Redis (spec §5) and map compact
-- hot-state keys back to entity IDs.

-- name: HotstateSiteRef :one
SELECT id, hkey, namespace_id, paused
FROM sites
WHERE id = $1;

-- name: HotstateListSites :many
SELECT id, hkey, namespace_id
FROM sites
ORDER BY hkey;

-- name: HotstateExistingSiteHkeys :many
SELECT hkey
FROM sites
WHERE hkey = ANY (@hkeys::bigint[]);

-- name: HotstateNamespaceSites :many
SELECT id, hkey, namespace_id, paused
FROM sites
WHERE namespace_id = $1
ORDER BY hkey;

-- name: HotstateIdentitiesByIDs :many
SELECT i.id,
       i.hkey,
       i.client,
       i.type_id,
       t.name    AS type_name,
       t.version AS type_version,
       i.state,
       i.ban_until,
       i.quarantine_until,
       i.region,
       i.payload_version,
       i.activated_at,
       a.hkey           AS account_hkey,
       a.state          AS account_state,
       a.ban_until      AS account_ban_until,
       a.cooldown_until AS account_cooldown_until,
       p.hkey           AS proxy_hkey,
       i.state_changed_at
FROM identities i
JOIN identity_types t ON t.id = i.type_id
LEFT JOIN accounts a ON a.id = i.account_id
LEFT JOIN proxy_bindings b ON b.identity_id = i.id
LEFT JOIN proxies p ON p.id = b.proxy_id
WHERE i.site_id = @site_id
  AND i.id = ANY (@ids::text[]);

-- name: HotstateIdentitiesPage :many
SELECT i.id,
       i.hkey,
       i.client,
       i.type_id,
       t.name    AS type_name,
       t.version AS type_version,
       i.state,
       i.ban_until,
       i.quarantine_until,
       i.region,
       i.payload_version,
       i.activated_at,
       a.hkey           AS account_hkey,
       a.state          AS account_state,
       a.ban_until      AS account_ban_until,
       a.cooldown_until AS account_cooldown_until,
       p.hkey           AS proxy_hkey,
       i.state_changed_at
FROM identities i
JOIN identity_types t ON t.id = i.type_id
LEFT JOIN accounts a ON a.id = i.account_id
LEFT JOIN proxy_bindings b ON b.identity_id = i.id
LEFT JOIN proxies p ON p.id = b.proxy_id
WHERE i.site_id = @site_id
  AND i.hkey > @after_hkey
ORDER BY i.hkey
LIMIT @page_size;

-- name: HotstateIdentityRef :one
SELECT id, hkey, client, state
FROM identities
WHERE id = @id
  AND site_id = @site_id;

-- name: HotstateLiveIdentityHkeys :many
SELECT hkey
FROM identities
WHERE site_id = @site_id
  AND state <> 'retired'
  AND hkey = ANY (@hkeys::bigint[]);

-- name: HotstateIdentityIDsByHkeys :many
SELECT hkey, id
FROM identities
WHERE site_id = @site_id
  AND hkey = ANY (@hkeys::bigint[]);

-- name: HotstateAccountStateTimes :many
-- Time of the last lifecycle change of accounts by hot-state key: accounts
-- have no state_changed_at column, but every account transition records a
-- non-shadow, non-cooldown state event in the same transaction.
SELECT a.hkey,
       COALESCE((SELECT e.created_at
                 FROM state_events AS e
                 WHERE e.subject_id = a.id
                   AND e.subject_kind = 'account'
                   AND e.shadow = false
                   AND e.action <> 'cooldown'
                 ORDER BY e.created_at DESC
                 LIMIT 1), a.created_at)::timestamptz AS state_changed_at
FROM accounts AS a
WHERE a.hkey = ANY (@hkeys::bigint[]);

-- name: HotstateAccountsByIDs :many
SELECT a.id, a.hkey, a.state, a.ban_until, a.cooldown_until,
       COALESCE((SELECT e.created_at
                 FROM state_events AS e
                 WHERE e.subject_id = a.id
                   AND e.subject_kind = 'account'
                   AND e.shadow = false
                   AND e.action <> 'cooldown'
                 ORDER BY e.created_at DESC
                 LIMIT 1), a.created_at)::timestamptz AS state_changed_at
FROM accounts AS a
WHERE a.site_id = @site_id
  AND a.id = ANY (@ids::text[]);

-- name: HotstateAccountMembers :many
SELECT account_id::text AS account_id, hkey
FROM identities
WHERE site_id = @site_id
  AND account_id = ANY (@account_ids::text[])
  AND state <> 'retired'
ORDER BY account_id, hkey;

-- name: HotstateProxiesByIDs :many
SELECT id, hkey, state, kind, region, provider, tags, max_concurrency, url_version, cooldown_until, ban_until,
       state_changed_at
FROM proxies
WHERE namespace_id = @namespace_id
  AND id = ANY (@ids::text[]);

-- name: HotstateProxiesPage :many
SELECT id, hkey, state, kind, region, provider, tags, max_concurrency, url_version, cooldown_until, ban_until,
       state_changed_at
FROM proxies
WHERE namespace_id = @namespace_id
  AND hkey > @after_hkey
ORDER BY hkey
LIMIT @page_size;

-- name: HotstateLiveProxyHkeys :many
SELECT hkey
FROM proxies
WHERE namespace_id = @namespace_id
  AND hkey = ANY (@hkeys::bigint[]);

-- name: HotstateProxyIDsByHkeys :many
SELECT hkey, id
FROM proxies
WHERE namespace_id = @namespace_id
  AND hkey = ANY (@hkeys::bigint[]);

-- name: HotstateProxyBindingsByProxyIDs :many
SELECT i.site_id, i.hkey AS identity_hkey, p.hkey AS proxy_hkey
FROM proxy_bindings b
JOIN identities i ON i.id = b.identity_id
JOIN proxies p ON p.id = b.proxy_id
WHERE b.proxy_id = ANY (@proxy_ids::text[])
ORDER BY i.site_id, i.hkey;

-- name: HotstateGroupIDsByHkeys :many
SELECT hkey, id
FROM endpoint_groups
WHERE site_id = @site_id
  AND hkey = ANY (@hkeys::bigint[]);

-- name: HotstateSiteGroupHkeys :many
SELECT hkey
FROM endpoint_groups
WHERE site_id = @site_id
ORDER BY hkey;

-- name: HotstateSnapshotIdentityEndpointPage :many
SELECT s.subject_id,
       s.endpoint_group_id,
       i.hkey AS identity_hkey,
       g.hkey AS group_hkey,
       s.score,
       s.samples,
       s.consecutive_failures,
       s.last_failure_at,
       s.cooldown_until,
       s.reuse_until,
       s.last_used_at,
       s.updated_at
FROM hot_state_snapshots s
JOIN identities i ON i.id = s.subject_id AND i.site_id = s.site_id
JOIN endpoint_groups g ON g.id = s.endpoint_group_id AND g.site_id = s.site_id
WHERE s.site_id = @site_id
  AND s.subject = 'ie'
  AND i.state <> 'retired'
  AND (s.subject_id > @after_subject_id::text
       OR (s.subject_id = @after_subject_id::text AND s.endpoint_group_id > @after_group_id::text))
ORDER BY s.subject_id, s.endpoint_group_id
LIMIT @page_size;

-- name: HotstateSnapshotIdentityGlobalPage :many
SELECT s.subject_id,
       i.hkey AS identity_hkey,
       s.score,
       s.samples,
       s.cooldown_until,
       s.reuse_until,
       s.last_used_at,
       s.updated_at
FROM hot_state_snapshots s
JOIN identities i ON i.id = s.subject_id AND i.site_id = s.site_id
WHERE s.site_id = @site_id
  AND s.subject = 'ig'
  AND s.endpoint_group_id = ''
  AND i.state <> 'retired'
  AND s.subject_id > @after_subject_id::text
ORDER BY s.subject_id
LIMIT @page_size;

-- name: HotstateSnapshotProxyPage :many
SELECT s.subject_id,
       p.hkey AS proxy_hkey,
       s.score,
       s.samples,
       s.consecutive_failures,
       s.last_failure_at,
       s.cooldown_until,
       s.updated_at
FROM hot_state_snapshots s
JOIN sites st ON st.id = s.site_id
JOIN proxies p ON p.id = s.subject_id AND p.namespace_id = st.namespace_id
WHERE s.site_id = @site_id
  AND s.subject = 'ps'
  AND s.endpoint_group_id = ''
  AND s.subject_id > @after_subject_id::text
ORDER BY s.subject_id
LIMIT @page_size;

-- name: HotstateDeleteIdentitySnapshots :exec
DELETE FROM hot_state_snapshots
WHERE site_id = @site_id
  AND subject IN ('ie', 'ig')
  AND subject_id = ANY (@identity_ids::text[]);

-- name: HotstateDeleteProxySnapshots :exec
DELETE FROM hot_state_snapshots
WHERE subject = 'ps'
  AND subject_id = ANY (@proxy_ids::text[]);
