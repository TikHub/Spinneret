-- Config center queries (private sqlc package configdb).

-- name: ConfigInsertItem :exec
INSERT INTO config_items (
    id, namespace_id, group_name, key, format, schema, description,
    draft_content, draft_updated_by, draft_updated_at, created_by
) VALUES (
    @id, @namespace_id, @group_name, @key, @format, sqlc.narg(schema), @description,
    sqlc.narg(draft_content), sqlc.narg(draft_updated_by), sqlc.narg(draft_updated_at), @created_by
);

-- name: ConfigGetItemHeader :one
SELECT id, namespace_id, group_name, key, format
FROM config_items
WHERE id = @id;

-- name: ConfigLockItem :one
SELECT id, namespace_id, group_name, key, format, schema, description, current_version,
       draft_content, draft_updated_by, draft_updated_at, created_by, created_at, updated_at
FROM config_items
WHERE id = @id
FOR UPDATE;

-- name: ConfigGetItemView :one
SELECT i.id, i.namespace_id, i.group_name, i.key, i.format, i.schema, i.description, i.current_version,
       i.draft_content, i.draft_updated_by, i.draft_updated_at, i.created_by, i.created_at, i.updated_at,
       v.content AS published_content, v.published_by, v.published_at
FROM config_items i
LEFT JOIN config_versions v ON v.item_id = i.id AND v.version = i.current_version
WHERE i.id = @id;

-- name: ConfigGetItemIDByLocator :one
SELECT id
FROM config_items
WHERE namespace_id = @namespace_id AND group_name = @group_name AND key = @key;

-- name: ConfigListItems :many
SELECT i.id, i.group_name, i.key, i.format, i.description, i.current_version,
       (i.draft_content IS NOT NULL)::boolean AS has_draft, i.draft_updated_by, i.draft_updated_at,
       i.created_at, i.updated_at, v.published_by, v.published_at
FROM config_items i
LEFT JOIN config_versions v ON v.item_id = i.id AND v.version = i.current_version
WHERE i.namespace_id = @namespace_id
  AND (sqlc.narg(group_name)::text IS NULL OR i.group_name = sqlc.narg(group_name)::text)
  AND (sqlc.narg(allowed_groups)::text[] IS NULL OR i.group_name = ANY (sqlc.narg(allowed_groups)::text[]))
  AND (sqlc.narg(search)::text IS NULL
       OR i.key ILIKE sqlc.narg(search)::text ESCAPE '\'
       OR i.description ILIKE sqlc.narg(search)::text ESCAPE '\')
  AND (sqlc.narg(after_group)::text IS NULL
       OR (i.group_name, i.key) > (sqlc.narg(after_group)::text, @after_key::text))
ORDER BY i.group_name, i.key
LIMIT @page_limit;

-- name: ConfigCountItems :one
SELECT count(*)
FROM config_items i
WHERE i.namespace_id = @namespace_id
  AND (sqlc.narg(group_name)::text IS NULL OR i.group_name = sqlc.narg(group_name)::text)
  AND (sqlc.narg(allowed_groups)::text[] IS NULL OR i.group_name = ANY (sqlc.narg(allowed_groups)::text[]))
  AND (sqlc.narg(search)::text IS NULL
       OR i.key ILIKE sqlc.narg(search)::text ESCAPE '\'
       OR i.description ILIKE sqlc.narg(search)::text ESCAPE '\');

-- name: ConfigListGroups :many
SELECT DISTINCT group_name
FROM config_items
WHERE namespace_id = @namespace_id
ORDER BY group_name
LIMIT @max_groups;

-- name: ConfigUpdateDraft :exec
UPDATE config_items
SET draft_content    = sqlc.narg(draft_content),
    draft_updated_by = sqlc.narg(draft_updated_by),
    draft_updated_at = sqlc.narg(draft_updated_at),
    schema           = sqlc.narg(schema),
    description      = @description,
    updated_at       = now()
WHERE id = @id;

-- name: ConfigInsertVersion :one
INSERT INTO config_versions (item_id, version, content, comment, source_version, published_by)
VALUES (@item_id, @version, @content, @comment, sqlc.narg(source_version), @published_by)
RETURNING published_at;

-- name: ConfigSetCurrentVersion :exec
UPDATE config_items
SET current_version  = @version,
    draft_content    = CASE WHEN @clear_draft::boolean THEN NULL ELSE draft_content END,
    draft_updated_by = CASE WHEN @clear_draft::boolean THEN NULL ELSE draft_updated_by END,
    draft_updated_at = CASE WHEN @clear_draft::boolean THEN NULL ELSE draft_updated_at END,
    updated_at       = now()
WHERE id = @id;

-- name: ConfigDeleteItem :execrows
DELETE FROM config_items
WHERE id = @id;

-- name: ConfigGetVersion :one
SELECT item_id, version, content, comment, source_version, published_by, published_at
FROM config_versions
WHERE item_id = @item_id AND version = @version;

-- name: ConfigListVersions :many
SELECT item_id, version, comment, source_version, published_by, published_at,
       octet_length(content)::bigint AS content_bytes
FROM config_versions
WHERE item_id = @item_id
  AND (@before_version::integer = 0 OR version < @before_version::integer)
ORDER BY version DESC
LIMIT @page_limit;

-- name: ConfigCountVersions :one
SELECT count(*)
FROM config_versions
WHERE item_id = @item_id;

-- name: ConfigCurrentVersions :many
SELECT i.group_name, i.key, i.id, i.current_version
FROM config_items i
JOIN (SELECT unnest(@group_names::text[]) AS group_name, unnest(@item_keys::text[]) AS item_key) AS r
  ON i.group_name = r.group_name AND i.key = r.item_key
WHERE i.namespace_id = @namespace_id;

-- name: ConfigVersionSizes :many
SELECT v.item_id, v.version, octet_length(v.content)::bigint AS content_bytes
FROM config_versions v
JOIN (SELECT unnest(@item_ids::text[]) AS item_id, unnest(@versions::integer[]) AS version) AS r
  ON v.item_id = r.item_id AND v.version = r.version;

-- name: ConfigVersionContents :many
SELECT v.item_id, v.version, i.format, v.content, v.published_at
FROM config_versions v
JOIN config_items i ON i.id = v.item_id
JOIN (SELECT unnest(@item_ids::text[]) AS item_id, unnest(@versions::integer[]) AS version) AS r
  ON v.item_id = r.item_id AND v.version = r.version;

-- name: ConfigExistingSecretPaths :many
SELECT path
FROM secrets
WHERE namespace_id = @namespace_id AND path = ANY (@paths::text[]);

-- name: ConfigExistingSecretVersions :many
SELECT s.path, sv.version
FROM secrets s
JOIN secret_versions sv ON sv.secret_id = s.id
JOIN (SELECT unnest(@paths::text[]) AS path, unnest(@versions::integer[]) AS version) AS r
  ON s.path = r.path AND sv.version = r.version
WHERE s.namespace_id = @namespace_id;

-- ConfigRaiseVersionFloor records the last version of an item that is being
-- deleted; floors never decrease.
-- name: ConfigRaiseVersionFloor :exec
INSERT INTO config_version_floors (namespace_id, group_name, key, last_version)
VALUES (@namespace_id, @group_name, @key, @last_version)
ON CONFLICT (namespace_id, group_name, key) DO UPDATE
SET last_version = GREATEST(config_version_floors.last_version, EXCLUDED.last_version),
    updated_at   = now();

-- ConfigVersionFloor returns the highest version ever published by a deleted
-- item of the same namespace, group and key (0 when none).
-- name: ConfigVersionFloor :one
SELECT coalesce(max(last_version), 0)::integer AS last_version
FROM config_version_floors
WHERE namespace_id = @namespace_id AND group_name = @group_name AND key = @key;
