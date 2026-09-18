-- Queries of the policy administration service (internal/policysvc).
-- Regenerate with: sqlc generate -f internal/policysvc/sqlc.yaml

-- name: PolicyGet :one
SELECT * FROM policies WHERE id = $1;

-- name: PolicyGetForUpdate :one
SELECT * FROM policies WHERE id = $1 FOR UPDATE;

-- name: PolicyGetByName :one
SELECT id, current_version
FROM policies
WHERE namespace_id = sqlc.arg(namespace_id) AND kind = sqlc.arg(kind) AND name = sqlc.arg(name);

-- name: PolicyLockKind :exec
-- Serializes publish, rollback and delete operations of one namespace and
-- kind so extends chains are validated against a stable set of policies.
SELECT pg_advisory_xact_lock(hashtextextended('spinneret:policy:' || sqlc.arg(namespace_id)::text || ':' || sqlc.arg(kind)::text, 0));

-- name: PolicyList :many
-- Lists policies without their YAML bodies. Site-restricted callers
-- (all_sites = false) only see policies bound at namespace level or on one of
-- site_ids. Keyset pagination on (kind, name).
SELECT p.id, p.namespace_id, p.kind, p.name, p.description, p.current_version,
       (p.draft_yaml IS NOT NULL)::boolean AS has_draft,
       p.draft_updated_by, p.draft_updated_at, p.created_by, p.created_at, p.updated_at
FROM policies p
WHERE p.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(kind)::text = '' OR p.kind = sqlc.arg(kind)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(p.name, sqlc.arg(search)::text) > 0)
  AND (sqlc.arg(all_sites)::boolean OR EXISTS (
        SELECT 1 FROM policy_bindings b
        WHERE b.policy_id = p.id
          AND (b.site_id IS NULL OR b.site_id = ANY (sqlc.arg(site_ids)::text[]))))
  AND (sqlc.arg(after_kind)::text = ''
       OR (p.kind, p.name) > (sqlc.arg(after_kind)::text, sqlc.arg(after_name)::text))
ORDER BY p.kind, p.name
LIMIT sqlc.arg(page_limit);

-- name: PolicyCount :one
SELECT count(*)
FROM policies p
WHERE p.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(kind)::text = '' OR p.kind = sqlc.arg(kind)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(p.name, sqlc.arg(search)::text) > 0)
  AND (sqlc.arg(all_sites)::boolean OR EXISTS (
        SELECT 1 FROM policy_bindings b
        WHERE b.policy_id = p.id
          AND (b.site_id IS NULL OR b.site_id = ANY (sqlc.arg(site_ids)::text[]))));

-- name: PolicyInsert :one
INSERT INTO policies (id, namespace_id, kind, name, description, draft_yaml, draft_updated_by, draft_updated_at, created_by)
VALUES (sqlc.arg(id), sqlc.arg(namespace_id), sqlc.arg(kind), sqlc.arg(name), sqlc.arg(description),
        sqlc.arg(draft_yaml)::text, sqlc.arg(created_by)::text, now(), sqlc.arg(created_by)::text)
RETURNING *;

-- name: PolicySaveDraft :one
UPDATE policies
SET draft_yaml       = sqlc.arg(draft_yaml)::text,
    draft_updated_by = sqlc.arg(actor)::text,
    draft_updated_at = now(),
    description      = CASE WHEN current_version = 0 THEN sqlc.arg(description)::text ELSE description END,
    updated_at       = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: PolicySetPublished :one
UPDATE policies
SET current_version  = sqlc.arg(version)::integer,
    description      = sqlc.arg(description)::text,
    draft_yaml       = CASE WHEN sqlc.arg(clear_draft)::boolean THEN NULL ELSE draft_yaml END,
    draft_updated_by = CASE WHEN sqlc.arg(clear_draft)::boolean THEN NULL ELSE draft_updated_by END,
    draft_updated_at = CASE WHEN sqlc.arg(clear_draft)::boolean THEN NULL ELSE draft_updated_at END,
    updated_at       = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: PolicyDelete :execrows
DELETE FROM policies WHERE id = $1;

-- name: PolicyInsertIfAbsent :many
INSERT INTO policies (id, namespace_id, kind, name, description, current_version, created_by)
VALUES (sqlc.arg(id), sqlc.arg(namespace_id), sqlc.arg(kind), sqlc.arg(name), sqlc.arg(description), 1, sqlc.arg(created_by))
ON CONFLICT (namespace_id, kind, name) DO NOTHING
RETURNING id;

-- name: PolicyMarkPublished :exec
UPDATE policies SET current_version = 1, updated_at = now() WHERE id = $1 AND current_version = 0;

-- name: PolicyPublishedSpecByName :one
SELECT p.id, p.name, p.current_version, v.spec
FROM policies p
JOIN policy_versions v ON v.policy_id = p.id AND v.version = p.current_version
WHERE p.namespace_id = sqlc.arg(namespace_id) AND p.kind = sqlc.arg(kind) AND p.name = sqlc.arg(name);

-- name: PolicyPublishedExtends :many
-- Lists (name, extends) of every published policy of a kind, bounded by
-- row_limit, for extends depth checks of descendants.
SELECT p.name, coalesce(v.spec ->> 'extends', '')::text AS extends
FROM policies p
JOIN policy_versions v ON v.policy_id = p.id AND v.version = p.current_version
WHERE p.namespace_id = sqlc.arg(namespace_id) AND p.kind = sqlc.arg(kind)
ORDER BY p.name
LIMIT sqlc.arg(row_limit);

-- name: PolicyPublishedChildren :many
SELECT p.name
FROM policies p
JOIN policy_versions v ON v.policy_id = p.id AND v.version = p.current_version
WHERE p.namespace_id = sqlc.arg(namespace_id) AND p.kind = sqlc.arg(kind)
  AND v.spec ->> 'extends' = sqlc.arg(name)::text
  AND p.id <> sqlc.arg(id)
ORDER BY p.name
LIMIT 10;

-- name: PolicyVersionInsert :exec
INSERT INTO policy_versions (policy_id, version, spec, spec_yaml, comment, created_by)
VALUES (sqlc.arg(policy_id), sqlc.arg(version), sqlc.arg(spec), sqlc.arg(spec_yaml), sqlc.arg(comment), sqlc.arg(created_by));

-- name: PolicyVersionInsertIfAbsent :execrows
INSERT INTO policy_versions (policy_id, version, spec, spec_yaml, comment, created_by)
VALUES (sqlc.arg(policy_id), sqlc.arg(version), sqlc.arg(spec), sqlc.arg(spec_yaml), sqlc.arg(comment), sqlc.arg(created_by))
ON CONFLICT (policy_id, version) DO NOTHING;

-- name: PolicyVersionGet :one
SELECT * FROM policy_versions WHERE policy_id = sqlc.arg(policy_id) AND version = sqlc.arg(version);

-- name: PolicyVersionList :many
SELECT * FROM policy_versions
WHERE policy_id = sqlc.arg(policy_id)
  AND (sqlc.arg(before_version)::integer = 0 OR version < sqlc.arg(before_version)::integer)
ORDER BY version DESC
LIMIT sqlc.arg(page_limit);

-- name: PolicyVersionCount :one
SELECT count(*) FROM policy_versions WHERE policy_id = $1;
