-- Read-only queries that load one namespace snapshot. They run inside a single
-- REPEATABLE READ, READ ONLY transaction so that every row belongs to the same
-- database snapshot.

-- name: CatalogListNamespaceIDs :many
SELECT id
FROM namespaces
ORDER BY id;

-- name: CatalogGetNamespace :one
SELECT n.id, n.tenant_id, t.name AS tenant_name, n.name, n.display_name
FROM namespaces n
JOIN tenants t ON t.id = n.tenant_id
WHERE n.id = sqlc.arg(id);

-- name: CatalogListSites :many
SELECT id, hkey, name, display_name, clients, paused, paused_reason
FROM sites
WHERE namespace_id = sqlc.arg(namespace_id)
ORDER BY name;

-- name: CatalogListEndpointGroups :many
SELECT eg.id, eg.hkey, eg.site_id, eg.client, eg.name, eg.low_watermark
FROM endpoint_groups eg
JOIN sites s ON s.id = eg.site_id
WHERE s.namespace_id = sqlc.arg(namespace_id)
ORDER BY eg.site_id, eg.client, eg.name;

-- name: CatalogListURIRules :many
SELECT r.id, r.endpoint_group_id, r.kind, r.pattern, r.position
FROM uri_rules r
JOIN endpoint_groups eg ON eg.id = r.endpoint_group_id
JOIN sites s ON s.id = eg.site_id
WHERE s.namespace_id = sqlc.arg(namespace_id)
ORDER BY r.endpoint_group_id, r.position, r.id;

-- name: CatalogListIdentityTypes :many
SELECT it.id, it.site_id, it.client, it.name, it.spec, it.version
FROM identity_types it
JOIN sites s ON s.id = it.site_id
WHERE s.namespace_id = sqlc.arg(namespace_id)
ORDER BY it.site_id, it.name;

-- CatalogListPublishedPolicies returns every policy of the namespace that has
-- a published version, with the spec of that version.
-- name: CatalogListPublishedPolicies :many
SELECT p.id, p.kind, p.name, p.current_version, v.spec
FROM policies p
JOIN policy_versions v ON v.policy_id = p.id AND v.version = p.current_version
WHERE p.namespace_id = sqlc.arg(namespace_id)
  AND p.current_version > 0
ORDER BY p.kind, p.name;

-- name: CatalogListPolicyBindings :many
SELECT id, policy_id, kind, site_id, client, endpoint_group_id
FROM policy_bindings
WHERE namespace_id = sqlc.arg(namespace_id)
ORDER BY created_at, id;
