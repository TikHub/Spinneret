-- name: IdentityTypeGet :one
SELECT t.id, t.site_id, t.client, t.name, t.description, t.spec, t.spec_yaml, t.json_schema, t.version,
       t.created_at, t.updated_at,
       (SELECT count(*) FROM identities i WHERE i.type_id = t.id AND i.state <> 'retired')::integer AS identity_count
FROM identity_types t
WHERE t.id = sqlc.arg(id);

-- name: IdentityTypeLock :one
SELECT id, site_id, client, name, version
FROM identity_types
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: IdentityTypeInsert :one
INSERT INTO identity_types (id, site_id, client, name, description, spec, spec_yaml, json_schema, version)
VALUES (sqlc.arg(id), sqlc.arg(site_id), sqlc.arg(client), sqlc.arg(name), sqlc.arg(description),
        sqlc.arg(spec), sqlc.arg(spec_yaml), sqlc.arg(json_schema), 1)
RETURNING created_at, updated_at;

-- name: IdentityTypeUpdate :one
UPDATE identity_types
SET description = sqlc.arg(description),
    spec = sqlc.arg(spec),
    spec_yaml = sqlc.arg(spec_yaml),
    json_schema = sqlc.arg(json_schema),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING version, created_at, updated_at;

-- name: IdentityTypeCountIdentities :one
SELECT count(*)::bigint AS n
FROM identities
WHERE type_id = sqlc.arg(type_id);

-- name: IdentityTypeDelete :execrows
DELETE FROM identity_types
WHERE id = sqlc.arg(id);

-- IdentityTypeList returns a page of identity types ordered by ID (UUIDv7,
-- i.e. creation order) after the given ID.
-- name: IdentityTypeList :many
SELECT t.id, t.site_id, t.client, t.name, t.description, t.spec, t.spec_yaml, t.json_schema, t.version,
       t.created_at, t.updated_at,
       (SELECT count(*) FROM identities i WHERE i.type_id = t.id AND i.state <> 'retired')::integer AS identity_count
FROM identity_types t
WHERE t.site_id = ANY (sqlc.arg(site_ids)::text[])
  AND (sqlc.arg(client)::text = '' OR t.client = sqlc.arg(client)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(t.name), lower(sqlc.arg(search)::text)) > 0)
  AND t.id > sqlc.arg(after_id)::text
ORDER BY t.id
LIMIT sqlc.arg(max_rows)::integer;

-- name: IdentityTypeCount :one
SELECT count(*)::bigint AS n
FROM identity_types t
WHERE t.site_id = ANY (sqlc.arg(site_ids)::text[])
  AND (sqlc.arg(client)::text = '' OR t.client = sqlc.arg(client)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(t.name), lower(sqlc.arg(search)::text)) > 0);

-- name: IdentityTypeSpecByID :one
SELECT id, site_id, spec, version
FROM identity_types
WHERE id = sqlc.arg(id);

-- name: IdentityTypeSpecByName :one
SELECT id, site_id, spec, version
FROM identity_types
WHERE site_id = sqlc.arg(site_id)
  AND name = sqlc.arg(name);

-- IdentityTypeLockSpec locks an identity type for a spec update. FOR NO KEY
-- UPDATE does not block concurrent identity inserts (their foreign key checks
-- take KEY SHARE locks).
-- name: IdentityTypeLockSpec :one
SELECT id, site_id, client, name, version, spec
FROM identity_types
WHERE id = sqlc.arg(id)
FOR NO KEY UPDATE;

-- name: IdentityTypeVersion :one
SELECT version
FROM identity_types
WHERE id = sqlc.arg(id);
