-- Queries of the proxy domain (internal/proxy). Generated into package proxydb
-- with: sqlc generate -f internal/proxy/sqlc.yaml

-- name: ProxyGet :one
SELECT * FROM proxies WHERE id = $1;

-- name: ProxyGetForUpdate :one
SELECT * FROM proxies WHERE id = $1 FOR UPDATE;

-- name: ProxyGetMany :many
SELECT * FROM proxies WHERE id = ANY(@ids::text[]) ORDER BY id;

-- name: ProxyGetManyForUpdate :many
SELECT * FROM proxies WHERE id = ANY(@ids::text[]) ORDER BY id FOR UPDATE;

-- name: ProxyFindByHashes :many
-- Only the columns import de-duplication needs (imports match up to 100 000 rows).
SELECT id, url_hash, kind, region, city, provider, max_concurrency, tags, session_template
FROM proxies
WHERE namespace_id = @namespace_id AND url_hash = ANY(@hashes::bytea[]);

-- name: ProxyInsertBatch :many
-- Tags are passed as comma-joined strings because PostgreSQL cannot unnest
-- arrays of arrays; tags never contain commas.
INSERT INTO proxies (
    id, namespace_id, scheme, host, port, username_hint, display_url, url_hash,
    url_ciphertext, url_wrapped_dek, url_kek_id, kind, region, city, provider,
    max_concurrency, tags, session_template
)
SELECT
    u.id, @namespace_id::text, u.scheme, u.host, u.port, u.username_hint, u.display_url, u.url_hash,
    u.url_ciphertext, u.url_wrapped_dek, u.url_kek_id, u.kind, u.region, u.city, u.provider,
    u.max_concurrency, CASE WHEN u.tags = '' THEN '{}'::text[] ELSE string_to_array(u.tags, ',') END,
    u.session_template
FROM (
    SELECT
        unnest(@ids::text[]) AS id,
        unnest(@schemes::text[]) AS scheme,
        unnest(@hosts::text[]) AS host,
        unnest(@ports::integer[]) AS port,
        unnest(@username_hints::text[]) AS username_hint,
        unnest(@display_urls::text[]) AS display_url,
        unnest(@url_hashes::bytea[]) AS url_hash,
        unnest(@url_ciphertexts::bytea[]) AS url_ciphertext,
        unnest(@url_wrapped_deks::bytea[]) AS url_wrapped_dek,
        unnest(@url_kek_ids::text[]) AS url_kek_id,
        unnest(@kinds::text[]) AS kind,
        unnest(@regions::text[]) AS region,
        unnest(@cities::text[]) AS city,
        unnest(@providers::text[]) AS provider,
        unnest(@max_concurrencies::integer[]) AS max_concurrency,
        unnest(@tags::text[]) AS tags,
        unnest(@session_templates::text[]) AS session_template
) AS u
ON CONFLICT (namespace_id, url_hash) DO NOTHING
RETURNING id;

-- name: ProxyUpdateAttributesBatch :exec
UPDATE proxies AS p SET
    kind = u.kind,
    region = u.region,
    city = u.city,
    provider = u.provider,
    max_concurrency = u.max_concurrency,
    tags = CASE WHEN u.tags = '' THEN '{}'::text[] ELSE string_to_array(u.tags, ',') END,
    session_template = u.session_template,
    updated_at = now()
FROM (
    SELECT
        unnest(@ids::text[]) AS id,
        unnest(@kinds::text[]) AS kind,
        unnest(@regions::text[]) AS region,
        unnest(@cities::text[]) AS city,
        unnest(@providers::text[]) AS provider,
        unnest(@max_concurrencies::integer[]) AS max_concurrency,
        unnest(@tags::text[]) AS tags,
        unnest(@session_templates::text[]) AS session_template
) AS u
WHERE p.id = u.id AND p.namespace_id = @namespace_id::text;

-- name: ProxyList :many
SELECT * FROM proxies
WHERE namespace_id = @namespace_id
  AND ((cardinality(@states::text[]) = 0 AND state <> 'retired') OR state = ANY(@states::text[]))
  AND (cardinality(@kinds::text[]) = 0 OR kind = ANY(@kinds::text[]))
  AND (cardinality(@providers::text[]) = 0 OR provider = ANY(@providers::text[]))
  AND (cardinality(@regions::text[]) = 0 OR region = ANY(@regions::text[]))
  AND tags @> @tags::text[]
  AND (@search_pattern::text = ''
       OR display_url ILIKE @search_pattern::text
       OR host ILIKE @search_pattern::text
       OR exit_ip ILIKE @search_pattern::text
       OR id LIKE @id_prefix_pattern::text)
  AND (@after_id::text = '' OR id < @after_id::text)
ORDER BY id DESC
LIMIT @page_limit::integer;

-- name: ProxyCount :one
SELECT count(*)::bigint FROM proxies
WHERE namespace_id = @namespace_id
  AND ((cardinality(@states::text[]) = 0 AND state <> 'retired') OR state = ANY(@states::text[]))
  AND (cardinality(@kinds::text[]) = 0 OR kind = ANY(@kinds::text[]))
  AND (cardinality(@providers::text[]) = 0 OR provider = ANY(@providers::text[]))
  AND (cardinality(@regions::text[]) = 0 OR region = ANY(@regions::text[]))
  AND tags @> @tags::text[]
  AND (@search_pattern::text = ''
       OR display_url ILIKE @search_pattern::text
       OR host ILIKE @search_pattern::text
       OR exit_ip ILIKE @search_pattern::text
       OR id LIKE @id_prefix_pattern::text);

-- name: ProxyBoundCounts :many
SELECT proxy_id, count(*)::integer AS bound
FROM proxy_bindings
WHERE proxy_id = ANY(@ids::text[])
GROUP BY proxy_id;

-- name: ProxyUpdate :one
UPDATE proxies SET
    scheme = @scheme,
    host = @host,
    port = @port,
    username_hint = @username_hint,
    display_url = @display_url,
    url_hash = @url_hash,
    url_ciphertext = @url_ciphertext,
    url_wrapped_dek = @url_wrapped_dek,
    url_kek_id = @url_kek_id,
    url_version = @url_version,
    kind = @kind,
    region = @region,
    city = @city,
    provider = @provider,
    max_concurrency = @max_concurrency,
    tags = @tags::text[],
    session_template = @session_template,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: ProxySetState :batchexec
UPDATE proxies SET
    state = @state,
    state_reason = @state_reason,
    state_changed_at = @state_changed_at,
    ban_until = @ban_until,
    cooldown_until = @cooldown_until,
    consecutive_check_failures = @consecutive_check_failures,
    next_check_at = @next_check_at,
    updated_at = @updated_at
WHERE id = @id;

-- name: ProxyInsertStateEvents :copyfrom
INSERT INTO state_events (
    id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
    from_state, to_state, action, scope, until, permanent, actor, reason, details
) VALUES (
    @id, @created_at, @tenant_id, @namespace_id, @site_id, @subject_kind, @subject_id,
    @from_state, @to_state, @action, @scope, @until, @permanent, @actor, @reason, @details
);

-- name: ProxyDeleteMany :many
DELETE FROM proxies WHERE id = ANY(@ids::text[]) RETURNING id, namespace_id, hkey, state, display_url;

-- name: ProxyProviderCounts :many
SELECT
    provider,
    (count(*) FILTER (WHERE state <> 'retired'))::integer AS proxies,
    (count(*) FILTER (WHERE state = 'active'))::integer AS active,
    (count(*) FILTER (WHERE state = 'dead'))::integer AS dead
FROM proxies
WHERE namespace_id = @namespace_id
GROUP BY provider;

-- name: ProxyProviderOutcomes :many
SELECT
    p.provider,
    coalesce(sum(o.count), 0)::bigint AS requests,
    coalesce(sum(o.count) FILTER (WHERE o.outcome = 'success'), 0)::bigint AS successes,
    coalesce(sum(o.count) FILTER (WHERE o.outcome = ANY(@risk_outcomes::text[])), 0)::bigint AS risks,
    coalesce(sum(o.latency_ms_sum), 0)::bigint AS latency_ms_sum
FROM outcome_stats_minutely AS o
JOIN proxies AS p ON p.id = o.proxy_id AND p.namespace_id = o.namespace_id
WHERE o.namespace_id = @namespace_id
  AND o.bucket >= @range_start::timestamptz
  AND o.bucket < @range_end::timestamptz
  AND o.proxy_id <> ''
  AND (@all_sites::boolean OR o.site_id = ANY(@site_ids::text[]))
GROUP BY p.provider;

-- name: ProxyDueForCheck :many
SELECT * FROM proxies
WHERE state IN ('active', 'dead')
  AND next_check_at <= @now::timestamptz
  AND (next_check_at, id) > (@after_at::timestamptz, @after_id::text)
ORDER BY next_check_at, id
LIMIT @page_limit::integer;

-- name: ProxyRecordCheck :exec
UPDATE proxies SET
    last_check_at = @checked_at,
    last_check_ok = @ok,
    last_latency_ms = @last_latency_ms,
    exit_ip = @exit_ip,
    consecutive_check_failures = @consecutive_check_failures,
    state = @state,
    state_reason = @state_reason,
    state_changed_at = @state_changed_at,
    next_check_at = @next_check_at,
    region = @region,
    city = @city,
    updated_at = @checked_at
WHERE id = @id;

-- name: ProxyCountByNamespaceState :many
SELECT namespace_id, state, count(*)::bigint AS proxies
FROM proxies
GROUP BY namespace_id, state;

-- name: ProxyResolveLoad :one
SELECT namespace_id, url_ciphertext, url_wrapped_dek, url_kek_id, url_version, kind, region, session_template
FROM proxies
WHERE id = $1;
