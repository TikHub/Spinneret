-- Policy binding queries of the policy administration service.

-- name: BindingListByNamespace :many
-- Lists the bindings of a namespace. site_id '' selects every binding,
-- otherwise the namespace-level bindings and those of that site.
SELECT b.id, b.policy_id, b.kind, b.namespace_id, b.site_id, b.client, b.endpoint_group_id,
       b.created_by, b.created_at, p.name AS policy_name
FROM policy_bindings b
JOIN policies p ON p.id = b.policy_id
WHERE b.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(kind)::text = '' OR b.kind = sqlc.arg(kind)::text)
  AND (sqlc.arg(site_id)::text = '' OR b.site_id IS NULL OR b.site_id = sqlc.arg(site_id)::text)
ORDER BY b.kind, b.site_id NULLS FIRST, b.client NULLS FIRST, b.endpoint_group_id NULLS FIRST
LIMIT sqlc.arg(row_limit);

-- name: BindingListEffective :many
-- Lists the bindings in effect for resolution below a site (site_id '' =
-- namespace level only): namespace-level bindings and those of the site whose
-- policy has a published version of the binding kind, mirroring the catalog.
SELECT b.policy_id, b.kind, b.site_id, b.client, b.endpoint_group_id,
       p.name AS policy_name, p.current_version
FROM policy_bindings b
JOIN policies p ON p.id = b.policy_id AND p.kind = b.kind
WHERE b.namespace_id = sqlc.arg(namespace_id)
  AND p.current_version > 0
  AND (b.site_id IS NULL OR b.site_id = sqlc.arg(site_id)::text)
ORDER BY b.created_at, b.id
LIMIT sqlc.arg(row_limit);

-- name: BindingListByPolicies :many
SELECT b.id, b.policy_id, b.kind, b.namespace_id, b.site_id, b.client, b.endpoint_group_id,
       b.created_by, b.created_at, p.name AS policy_name
FROM policy_bindings b
JOIN policies p ON p.id = b.policy_id
WHERE b.policy_id = ANY (sqlc.arg(policy_ids)::text[])
ORDER BY b.policy_id, b.site_id NULLS FIRST, b.client NULLS FIRST, b.endpoint_group_id NULLS FIRST
LIMIT sqlc.arg(row_limit);

-- name: BindingGet :one
SELECT b.id, b.policy_id, b.kind, b.namespace_id, b.site_id, b.client, b.endpoint_group_id,
       b.created_by, b.created_at, p.name AS policy_name
FROM policy_bindings b
JOIN policies p ON p.id = b.policy_id
WHERE b.id = $1;

-- name: BindingGetByTarget :one
SELECT id, policy_id
FROM policy_bindings
WHERE namespace_id = sqlc.arg(namespace_id)
  AND kind = sqlc.arg(kind)
  AND coalesce(site_id, '') = sqlc.arg(site_id)::text
  AND coalesce(client, '') = sqlc.arg(client)::text
  AND coalesce(endpoint_group_id, '') = sqlc.arg(endpoint_group_id)::text
FOR UPDATE;

-- name: BindingUpsert :one
INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id, client, endpoint_group_id, created_by)
VALUES (sqlc.arg(id), sqlc.arg(policy_id), sqlc.arg(kind), sqlc.arg(namespace_id),
        sqlc.narg(site_id), sqlc.narg(client), sqlc.narg(endpoint_group_id), sqlc.arg(created_by))
ON CONFLICT (namespace_id, kind, coalesce(site_id, ''), coalesce(client, ''), coalesce(endpoint_group_id, ''))
DO UPDATE SET policy_id = EXCLUDED.policy_id, created_by = EXCLUDED.created_by, created_at = now()
RETURNING *;

-- name: BindingInsertIfAbsent :execrows
INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, created_by)
VALUES (sqlc.arg(id), sqlc.arg(policy_id), sqlc.arg(kind), sqlc.arg(namespace_id), sqlc.arg(created_by))
ON CONFLICT (namespace_id, kind, coalesce(site_id, ''), coalesce(client, ''), coalesce(endpoint_group_id, ''))
DO NOTHING;

-- name: BindingDelete :execrows
-- Deletes a binding only while it still binds the expected policy (a binding
-- keeps its id when the policy at its target is replaced).
DELETE FROM policy_bindings WHERE id = sqlc.arg(id) AND policy_id = sqlc.arg(policy_id);

-- name: BindingNamespaceLevelExists :one
SELECT EXISTS (
    SELECT 1 FROM policy_bindings
    WHERE policy_id = $1 AND site_id IS NULL AND client IS NULL AND endpoint_group_id IS NULL
)::boolean;
