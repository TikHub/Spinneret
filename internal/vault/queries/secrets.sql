-- Secrets and secret versions (vault-secrets track).

-- name: VaultSecretInsert :one
INSERT INTO secrets (id, namespace_id, path, description, tags, current_version, expires_at, created_by)
VALUES (
    sqlc.arg(id),
    sqlc.arg(namespace_id),
    sqlc.arg(path),
    sqlc.arg(description),
    sqlc.arg(tags)::text[],
    1,
    sqlc.narg(expires_at)::timestamptz,
    sqlc.arg(created_by)
)
RETURNING created_at, updated_at;

-- name: VaultSecretVersionInsert :exec
INSERT INTO secret_versions (secret_id, version, ciphertext, wrapped_dek, kek_id, created_by)
VALUES (
    sqlc.arg(secret_id),
    sqlc.arg(version),
    sqlc.arg(ciphertext),
    sqlc.arg(wrapped_dek),
    sqlc.arg(kek_id),
    sqlc.arg(created_by)
);

-- VaultSecretGet loads a secret with its namespace (for authorization) and
-- the envelope of its current version (for masking).
-- name: VaultSecretGet :one
SELECT s.id,
       s.namespace_id,
       n.tenant_id,
       n.name AS namespace_name,
       s.path,
       s.description,
       s.tags,
       s.current_version,
       s.expires_at,
       s.last_accessed_at,
       s.created_by,
       s.created_at,
       s.updated_at,
       v.ciphertext,
       v.wrapped_dek,
       v.kek_id
FROM secrets s
JOIN namespaces n ON n.id = s.namespace_id
LEFT JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.id = sqlc.arg(id);

-- VaultSecretLock loads the authorization data of a secret and locks its row
-- until the end of the transaction.
-- name: VaultSecretLock :one
SELECT s.id,
       s.namespace_id,
       n.tenant_id,
       n.name AS namespace_name,
       s.path,
       s.current_version
FROM secrets s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.id = sqlc.arg(id)
FOR UPDATE OF s;

-- name: VaultSecretUpdate :exec
UPDATE secrets
SET current_version = sqlc.arg(current_version),
    description     = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
    tags            = CASE WHEN sqlc.arg(set_tags)::boolean THEN sqlc.arg(tags)::text[] ELSE tags END,
    expires_at      = CASE WHEN sqlc.arg(set_expires_at)::boolean THEN sqlc.narg(expires_at)::timestamptz ELSE expires_at END,
    updated_at      = now()
WHERE id = sqlc.arg(id);

-- name: VaultSecretDelete :execrows
DELETE FROM secrets
WHERE id = sqlc.arg(id);

-- VaultSecretReadByPath loads one version (0 = current) of a secret addressed
-- by namespace and path.
-- name: VaultSecretReadByPath :one
SELECT s.id,
       s.path,
       s.expires_at,
       v.version,
       v.ciphertext,
       v.wrapped_dek,
       v.kek_id
FROM secrets s
JOIN secret_versions v ON v.secret_id = s.id
WHERE s.namespace_id = sqlc.arg(namespace_id)
  AND s.path = sqlc.arg(path)
  AND v.version = CASE WHEN sqlc.arg(version)::integer > 0 THEN sqlc.arg(version)::integer ELSE s.current_version END;

-- name: VaultSecretIDByPath :one
SELECT id
FROM secrets
WHERE namespace_id = sqlc.arg(namespace_id)
  AND path = sqlc.arg(path);

-- name: VaultSecretVersionGet :one
SELECT version, ciphertext, wrapped_dek, kek_id
FROM secret_versions
WHERE secret_id = sqlc.arg(secret_id)
  AND version = sqlc.arg(version);

-- VaultSecretList returns one keyset page of secrets ordered by path together
-- with the envelope of their current version.
-- name: VaultSecretList :many
SELECT s.id,
       s.namespace_id,
       s.path,
       s.description,
       s.tags,
       s.current_version,
       s.expires_at,
       s.last_accessed_at,
       s.created_by,
       s.created_at,
       s.updated_at,
       v.ciphertext,
       v.wrapped_dek,
       v.kek_id
FROM secrets s
LEFT JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(prefix)::text = '' OR starts_with(s.path, sqlc.arg(prefix)::text))
  AND (sqlc.arg(search)::text = ''
       OR strpos(lower(s.path), lower(sqlc.arg(search)::text)) > 0
       OR strpos(lower(s.description), lower(sqlc.arg(search)::text)) > 0)
  AND (cardinality(sqlc.arg(tags)::text[]) = 0 OR s.tags @> sqlc.arg(tags)::text[])
  AND s.path > sqlc.arg(after_path)::text
ORDER BY s.path
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultSecretCount :one
SELECT count(*)::bigint AS total
FROM secrets s
WHERE s.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(prefix)::text = '' OR starts_with(s.path, sqlc.arg(prefix)::text))
  AND (sqlc.arg(search)::text = ''
       OR strpos(lower(s.path), lower(sqlc.arg(search)::text)) > 0
       OR strpos(lower(s.description), lower(sqlc.arg(search)::text)) > 0)
  AND (cardinality(sqlc.arg(tags)::text[]) = 0 OR s.tags @> sqlc.arg(tags)::text[]);

-- name: VaultSecretVersionList :many
SELECT version, kek_id, created_by, created_at
FROM secret_versions
WHERE secret_id = sqlc.arg(secret_id)
  AND (sqlc.arg(before_version)::integer <= 0 OR version < sqlc.arg(before_version)::integer)
ORDER BY version DESC
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultSecretVersionCount :one
SELECT count(*)::bigint AS total
FROM secret_versions
WHERE secret_id = sqlc.arg(secret_id);

-- VaultSecretAccessLogs returns one keyset page of the audit entries of a
-- secret, newest first. since bounds the scanned partitions.
-- name: VaultSecretAccessLogs :many
SELECT id,
       created_at,
       actor_kind,
       actor_id,
       actor_name,
       action,
       result,
       ip,
       COALESCE(details ->> 'version', '')::text AS version
FROM audit_logs
WHERE tenant_id = sqlc.arg(tenant_id)
  AND resource_kind = 'secret'
  AND resource_id = sqlc.arg(secret_id)
  AND created_at >= sqlc.arg(since)::timestamptz
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::text))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_rows)::integer;

-- VaultSecretTouch advances last_accessed_at of many secrets at once; it
-- never moves the timestamp backwards.
-- name: VaultSecretTouch :execrows
UPDATE secrets AS s
SET last_accessed_at = GREATEST(COALESCE(s.last_accessed_at, a.accessed_at), a.accessed_at)
FROM (
    SELECT unnest(sqlc.arg(ids)::text[]) AS id,
           unnest(sqlc.arg(accessed_at)::timestamptz[]) AS accessed_at
) AS a
WHERE s.id = a.id;
