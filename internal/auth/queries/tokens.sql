-- API token queries of the auth track.

-- name: AuthTokenByHash :one
SELECT t.id, t.tenant_id, t.namespace_id, n.name AS namespace_name, t.name, t.scopes, t.ip_allowlist,
       t.rate_limit_rps, t.expires_at, t.revoked_at
FROM api_tokens t
JOIN namespaces n ON n.id = t.namespace_id
WHERE t.token_hash = $1;

-- name: AuthTokenInsert :one
INSERT INTO api_tokens (
    id, tenant_id, namespace_id, name, description, token_prefix, token_hash, scopes, ip_allowlist,
    rate_limit_rps, expires_at, created_by
) VALUES (
    @id, @tenant_id, @namespace_id, @name, @description, @token_prefix, @token_hash, @scopes, @ip_allowlist,
    @rate_limit_rps, sqlc.narg('expires_at'), @created_by
)
RETURNING id, tenant_id, namespace_id, name, description, token_prefix, scopes, ip_allowlist, rate_limit_rps,
          expires_at, revoked_at, last_used_at, last_used_ip, created_by, created_at;

-- name: AuthTokenGet :one
SELECT t.id, t.tenant_id, t.namespace_id, n.name AS namespace_name, t.name, t.description, t.token_prefix,
       t.scopes, t.ip_allowlist, t.rate_limit_rps, t.expires_at, t.revoked_at, t.last_used_at, t.last_used_ip,
       t.created_by, t.created_at
FROM api_tokens t
JOIN namespaces n ON n.id = t.namespace_id
WHERE t.id = $1;

-- name: AuthTokenRevoke :one
UPDATE api_tokens
SET revoked_at = coalesce(revoked_at, now())
WHERE id = $1
RETURNING revoked_at;

-- name: AuthTokenList :many
SELECT t.id, t.tenant_id, t.namespace_id, n.name AS namespace_name, t.name, t.description, t.token_prefix,
       t.scopes, t.ip_allowlist, t.rate_limit_rps, t.expires_at, t.revoked_at, t.last_used_at, t.last_used_ip,
       t.created_by, t.created_at
FROM api_tokens t
JOIN namespaces n ON n.id = t.namespace_id
WHERE t.namespace_id = ANY(@namespace_ids::text[])
  AND (@include_revoked::boolean OR t.revoked_at IS NULL)
  AND (NOT @has_cursor::boolean OR (t.created_at, t.id) < (@cursor_created_at::timestamptz, @cursor_id::text))
ORDER BY t.created_at DESC, t.id DESC
LIMIT @page_limit;

-- name: AuthTokenCount :one
SELECT count(*)::integer AS total
FROM api_tokens t
WHERE t.namespace_id = ANY(@namespace_ids::text[])
  AND (@include_revoked::boolean OR t.revoked_at IS NULL);

-- name: AuthTokenTouch :exec
UPDATE api_tokens AS t
SET last_used_at = v.used_at,
    last_used_ip = v.used_ip
FROM (
    SELECT unnest(@ids::text[]) AS id,
           unnest(@used_at::timestamptz[]) AS used_at,
           unnest(@used_ip::text[]) AS used_ip
) AS v
WHERE t.id = v.id
  AND (t.last_used_at IS NULL OR t.last_used_at < v.used_at);

-- name: AuthNamespaceGet :one
SELECT id, tenant_id, name
FROM namespaces
WHERE id = $1;

-- name: AuthNamespacesOfTenant :many
SELECT id, tenant_id, name
FROM namespaces
WHERE tenant_id = $1
ORDER BY name;
