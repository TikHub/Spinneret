-- IdentityGet returns one identity with its type name, account reference, the
-- last hot-state snapshot of its global score (default baseline 70 when no
-- snapshot exists) and its bound proxy.
-- name: IdentityGet :one
SELECT i.id, i.site_id, i.client, i.type_id, t.name AS type_name, i.account_id,
       coalesce(a.external_ref, '')::text AS account_ref,
       i.state, i.state_reason, i.state_changed_at, i.ban_until, i.quarantine_until, i.region, i.tags, i.labels,
       i.payload_version, i.activated_at, i.last_used_at,
       coalesce(h.score, 70)::double precision AS global_score,
       coalesce(h.samples, 0)::integer AS global_samples,
       coalesce(pb.proxy_id, '')::text AS bound_proxy_id,
       i.created_at, i.updated_at
FROM identities i
JOIN identity_types t ON t.id = i.type_id
LEFT JOIN accounts a ON a.id = i.account_id
LEFT JOIN hot_state_snapshots h
       ON h.site_id = i.site_id AND h.subject = 'ig' AND h.subject_id = i.id AND h.endpoint_group_id = ''
LEFT JOIN proxy_bindings pb ON pb.identity_id = i.id
WHERE i.id = sqlc.arg(id);

-- name: IdentityLocate :one
SELECT id, site_id, type_id, account_id
FROM identities
WHERE id = sqlc.arg(id);

-- name: IdentityLocateMany :many
SELECT id, site_id
FROM identities
WHERE id = ANY (sqlc.arg(ids)::text[]);

-- IdentityLock locks one identity and returns every column a payload or
-- attribute update needs to compute the new row.
-- name: IdentityLock :one
SELECT id, site_id, client, type_id, account_id, state, state_reason, state_changed_at, quarantine_until,
       region, tags, labels, unique_hash, payload_hash, payload_version, activated_at
FROM identities
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- IdentityLockByHashes locks identities in ID order, so concurrent writers
-- that also lock in ID order cannot deadlock.
-- name: IdentityLockByHashes :many
SELECT id, site_id, client, type_id, account_id, state, state_reason, state_changed_at, quarantine_until,
       region, tags, labels, unique_hash, payload_hash, payload_version, activated_at
FROM identities
WHERE type_id = sqlc.arg(type_id)
  AND unique_hash = ANY (sqlc.arg(hashes)::bytea[])
ORDER BY id
FOR UPDATE;

-- name: IdentityFindByHashes :many
SELECT id, site_id, client, type_id, account_id, state, state_reason, state_changed_at, quarantine_until,
       region, tags, labels, unique_hash, payload_hash, payload_version, activated_at
FROM identities
WHERE type_id = sqlc.arg(type_id)
  AND unique_hash = ANY (sqlc.arg(hashes)::bytea[]);

-- IdentityWrite stores the mutable columns of an identity computed by the
-- service from a locked row.
-- name: IdentityWrite :batchexec
UPDATE identities
SET account_id = sqlc.narg(account_id),
    state = sqlc.arg(state),
    state_reason = sqlc.arg(state_reason),
    state_changed_at = sqlc.arg(state_changed_at),
    quarantine_until = sqlc.narg(quarantine_until),
    region = sqlc.arg(region),
    tags = sqlc.arg(tags),
    labels = sqlc.arg(labels),
    unique_hash = sqlc.arg(unique_hash),
    payload_hash = sqlc.arg(payload_hash),
    payload_version = sqlc.arg(payload_version),
    activated_at = sqlc.narg(activated_at),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: IdentityCopy :copyfrom
INSERT INTO identities (id, site_id, client, type_id, account_id, state, state_reason, region, tags, labels,
                        unique_hash, payload_hash, payload_version, activated_at, created_by)
VALUES (sqlc.arg(id), sqlc.arg(site_id), sqlc.arg(client), sqlc.arg(type_id), sqlc.narg(account_id),
        sqlc.arg(state), sqlc.arg(state_reason), sqlc.arg(region), sqlc.arg(tags), sqlc.arg(labels),
        sqlc.arg(unique_hash), sqlc.arg(payload_hash), sqlc.arg(payload_version), sqlc.narg(activated_at),
        sqlc.arg(created_by));

-- name: IdentityPayloadCopy :copyfrom
INSERT INTO identity_payloads (identity_id, version, ciphertext, wrapped_dek, kek_id, created_by)
VALUES (sqlc.arg(identity_id), sqlc.arg(version), sqlc.arg(ciphertext), sqlc.arg(wrapped_dek), sqlc.arg(kek_id),
        sqlc.arg(created_by));

-- IdentityPayloadPrune deletes payload versions older than min_version for
-- each (identity_id, min_version) pair.
-- name: IdentityPayloadPrune :execrows
DELETE FROM identity_payloads p
USING (SELECT unnest(sqlc.arg(identity_ids)::text[]) AS identity_id,
              unnest(sqlc.arg(min_versions)::integer[]) AS min_version) u
WHERE p.identity_id = u.identity_id
  AND p.version < u.min_version;

-- name: IdentityPayloadGet :one
SELECT ciphertext, wrapped_dek, kek_id
FROM identity_payloads
WHERE identity_id = sqlc.arg(identity_id)
  AND version = sqlc.arg(version);

-- name: IdentityPayloadVersions :many
SELECT version
FROM identity_payloads
WHERE identity_id = sqlc.arg(identity_id)
ORDER BY version;

-- IdentityTypeAdvisoryLock serializes payload writes of one identity type
-- (imports and payload updates) until the end of the transaction, so unique
-- key decisions never race.
-- name: IdentityTypeAdvisoryLock :exec
SELECT pg_advisory_xact_lock(hashtextextended('spinneret:identity-type:' || sqlc.arg(type_id)::text, 0));

-- IdentityScrambleUniqueHashes replaces the unique hashes of every identity of
-- a type with per-identity placeholders, so a following rehash cannot collide
-- with hashes that are about to be replaced.
-- name: IdentityScrambleUniqueHashes :execrows
UPDATE identities
SET unique_hash = sha256(convert_to('spinneret:rehash:' || id, 'UTF8'))
WHERE type_id = sqlc.arg(type_id);

-- IdentityRehashBatch returns a page (in ID order) of identities of a type
-- with their current sealed payload, locking the identities.
-- name: IdentityRehashBatch :many
SELECT i.id, i.payload_version, p.ciphertext, p.wrapped_dek, p.kek_id
FROM identities i
LEFT JOIN identity_payloads p ON p.identity_id = i.id AND p.version = i.payload_version
WHERE i.type_id = sqlc.arg(type_id)
  AND i.id > sqlc.arg(after_id)::text
ORDER BY i.id
LIMIT sqlc.arg(max_rows)::integer
FOR UPDATE OF i;

-- name: IdentitySetUniqueHashes :execrows
UPDATE identities i
SET unique_hash = u.unique_hash
FROM (SELECT unnest(sqlc.arg(ids)::text[]) AS id, unnest(sqlc.arg(hashes)::bytea[]) AS unique_hash) u
WHERE i.id = u.id;

-- name: SecretPathsExisting :many
-- Paths of the given secret paths that exist in a namespace (write-time check
-- of secret_ref payload fields).
SELECT path
FROM secrets
WHERE namespace_id = @namespace_id
  AND path = ANY (@paths::text[]);
