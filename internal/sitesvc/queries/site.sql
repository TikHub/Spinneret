-- name: SiteInsert :one
INSERT INTO sites (id, namespace_id, name, display_name, description, clients)
VALUES (sqlc.arg(id), sqlc.arg(namespace_id), sqlc.arg(name), sqlc.arg(display_name), sqlc.arg(description),
        sqlc.arg(clients)::text[])
RETURNING id, hkey, namespace_id, name, display_name, description, clients, paused, paused_reason, paused_at,
          created_at, updated_at;

-- SiteGet returns a site with its namespace, tenant and counts.
-- name: SiteGet :one
SELECT s.id, s.hkey, s.namespace_id, n.name AS namespace_name, n.tenant_id, s.name, s.display_name, s.description,
       s.clients, s.paused, s.paused_reason, s.paused_at, s.created_at, s.updated_at,
       (SELECT count(*) FROM endpoint_groups eg WHERE eg.site_id = s.id)::integer AS endpoint_group_count,
       (SELECT count(*) FROM identities i WHERE i.site_id = s.id AND i.state <> 'retired')::integer AS identity_count
FROM sites s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.id = sqlc.arg(id);

-- name: SiteGetRefByName :one
SELECT s.id, s.hkey, s.namespace_id, n.name AS namespace_name, n.tenant_id, s.name
FROM sites s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.namespace_id = sqlc.arg(namespace_id) AND s.name = sqlc.arg(name);

-- name: SiteGetRef :one
SELECT s.id, s.hkey, s.namespace_id, n.name AS namespace_name, n.tenant_id, s.name
FROM sites s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.id = sqlc.arg(id);

-- SiteLock locks the site row for the rest of the transaction.
-- name: SiteLock :one
SELECT id, hkey, namespace_id, name, clients
FROM sites
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- SiteList returns one page of sites ordered by name. When all_sites is false
-- only the listed site IDs are returned.
-- name: SiteList :many
SELECT s.id, s.hkey, s.namespace_id, n.name AS namespace_name, n.tenant_id, s.name, s.display_name, s.description,
       s.clients, s.paused, s.paused_reason, s.paused_at, s.created_at, s.updated_at,
       (SELECT count(*) FROM endpoint_groups eg WHERE eg.site_id = s.id)::integer AS endpoint_group_count,
       (SELECT count(*) FROM identities i WHERE i.site_id = s.id AND i.state <> 'retired')::integer AS identity_count
FROM sites s
JOIN namespaces n ON n.id = s.namespace_id
WHERE s.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(all_sites)::boolean OR s.id = ANY (sqlc.arg(site_ids)::text[]))
  AND s.name > sqlc.arg(after_name)
ORDER BY s.name
LIMIT sqlc.arg(max_rows);

-- name: SiteCount :one
SELECT count(*)::integer
FROM sites s
WHERE s.namespace_id = sqlc.arg(namespace_id)
  AND (sqlc.arg(all_sites)::boolean OR s.id = ANY (sqlc.arg(site_ids)::text[]));

-- name: SiteUpdate :exec
UPDATE sites
SET display_name = sqlc.arg(display_name),
    description  = sqlc.arg(description),
    clients      = sqlc.arg(clients)::text[],
    updated_at   = now()
WHERE id = sqlc.arg(id);

-- name: SiteDelete :execrows
DELETE FROM sites
WHERE id = sqlc.arg(id);

-- name: SiteCountIdentities :one
SELECT count(*)::integer
FROM identities
WHERE site_id = sqlc.arg(site_id);

-- name: SiteDeleteIdentities :execrows
DELETE FROM identities
WHERE site_id = sqlc.arg(site_id);

-- SiteCountClientUsage counts identity types and identities of the given
-- clients of a site.
-- name: SiteCountClientUsage :one
SELECT (SELECT count(*) FROM identity_types it
        WHERE it.site_id = sqlc.arg(site_id) AND it.client = ANY (sqlc.arg(clients)::text[]))::integer AS identity_types,
       (SELECT count(*) FROM identities i
        WHERE i.site_id = sqlc.arg(site_id) AND i.client = ANY (sqlc.arg(clients)::text[]))::integer AS identities;

-- SiteDeleteClientBindings removes client-level policy bindings of removed
-- clients (endpoint-group bindings are removed with their groups).
-- name: SiteDeleteClientBindings :execrows
DELETE FROM policy_bindings
WHERE site_id = sqlc.arg(site_id)::text
  AND client = ANY (sqlc.arg(clients)::text[])
  AND endpoint_group_id IS NULL;

-- name: EndpointGroupInsert :one
INSERT INTO endpoint_groups (id, site_id, client, name, description, low_watermark)
VALUES (sqlc.arg(id), sqlc.arg(site_id), sqlc.arg(client), sqlc.arg(name), sqlc.arg(description),
        sqlc.arg(low_watermark))
RETURNING id, hkey, site_id, client, name, description, low_watermark, created_at, updated_at;

-- EndpointGroupInsertDefaults creates the "_default" group of every listed
-- client that does not have one yet.
-- name: EndpointGroupInsertDefaults :execrows
INSERT INTO endpoint_groups (id, site_id, client, name)
SELECT unnest(sqlc.arg(ids)::text[]), sqlc.arg(site_id)::text, unnest(sqlc.arg(clients)::text[]), '_default'
ON CONFLICT (site_id, client, name) DO NOTHING;

-- name: EndpointGroupGet :one
SELECT eg.id, eg.hkey, eg.site_id, s.hkey AS site_hkey, s.name AS site_name, s.namespace_id,
       n.name AS namespace_name, n.tenant_id, eg.client, eg.name, eg.description, eg.low_watermark,
       eg.created_at, eg.updated_at
FROM endpoint_groups eg
JOIN sites s ON s.id = eg.site_id
JOIN namespaces n ON n.id = s.namespace_id
WHERE eg.id = sqlc.arg(id);

-- name: EndpointGroupList :many
SELECT eg.id, eg.hkey, eg.site_id, eg.client, eg.name, eg.description, eg.low_watermark, eg.created_at, eg.updated_at
FROM endpoint_groups eg
WHERE eg.site_id = sqlc.arg(site_id)
  AND (sqlc.arg(client)::text = '' OR eg.client = sqlc.arg(client)::text)
  AND (eg.client, eg.name) > (sqlc.arg(after_client)::text, sqlc.arg(after_name)::text)
ORDER BY eg.client, eg.name
LIMIT sqlc.arg(max_rows);

-- name: EndpointGroupCount :one
SELECT count(*)::integer
FROM endpoint_groups eg
WHERE eg.site_id = sqlc.arg(site_id)
  AND (sqlc.arg(client)::text = '' OR eg.client = sqlc.arg(client)::text);

-- name: EndpointGroupUpdate :exec
UPDATE endpoint_groups
SET description   = sqlc.arg(description),
    low_watermark = sqlc.arg(low_watermark),
    updated_at    = now()
WHERE id = sqlc.arg(id);

-- name: EndpointGroupTouch :exec
UPDATE endpoint_groups
SET updated_at = now()
WHERE id = sqlc.arg(id);

-- name: EndpointGroupDelete :execrows
DELETE FROM endpoint_groups
WHERE id = sqlc.arg(id);

-- name: EndpointGroupDeleteByClients :execrows
DELETE FROM endpoint_groups
WHERE site_id = sqlc.arg(site_id)
  AND client = ANY (sqlc.arg(clients)::text[]);

-- name: URIRuleListByGroups :many
SELECT id, endpoint_group_id, kind, pattern, position
FROM uri_rules
WHERE endpoint_group_id = ANY (sqlc.arg(group_ids)::text[])
ORDER BY endpoint_group_id, position, id;

-- URIRuleListBySiteClient returns the rules of every endpoint group of a site
-- client, with their group names.
-- name: URIRuleListBySiteClient :many
SELECT r.id, r.endpoint_group_id, eg.name AS group_name, r.kind, r.pattern, r.position
FROM uri_rules r
JOIN endpoint_groups eg ON eg.id = r.endpoint_group_id
WHERE eg.site_id = sqlc.arg(site_id) AND eg.client = sqlc.arg(client)
ORDER BY r.endpoint_group_id, r.position, r.id;

-- name: URIRuleListPage :many
SELECT id, endpoint_group_id, kind, pattern, position
FROM uri_rules
WHERE endpoint_group_id = sqlc.arg(endpoint_group_id)
  AND (position, id) > (sqlc.arg(after_position)::integer, sqlc.arg(after_id)::text)
ORDER BY position, id
LIMIT sqlc.arg(max_rows);

-- name: URIRuleCount :one
SELECT count(*)::integer
FROM uri_rules
WHERE endpoint_group_id = sqlc.arg(endpoint_group_id);

-- name: URIRuleDeleteByGroup :execrows
DELETE FROM uri_rules
WHERE endpoint_group_id = sqlc.arg(endpoint_group_id);

-- name: URIRuleInsertBatch :execrows
INSERT INTO uri_rules (id, endpoint_group_id, kind, pattern, position)
SELECT unnest(sqlc.arg(ids)::text[]), sqlc.arg(endpoint_group_id)::text, unnest(sqlc.arg(kinds)::text[]),
       unnest(sqlc.arg(patterns)::text[]), unnest(sqlc.arg(positions)::integer[]);
