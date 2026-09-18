-- name: NotifyChannelInsert :one
INSERT INTO notification_channels (
    id, tenant_id, namespace_id, name, kind,
    config_ciphertext, config_wrapped_dek, config_kek_id,
    event_types, site_ids, min_severity, enabled, created_by
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.narg(namespace_id), sqlc.arg(name), sqlc.arg(kind),
    sqlc.arg(config_ciphertext), sqlc.arg(config_wrapped_dek), sqlc.arg(config_kek_id),
    sqlc.arg(event_types)::text[], sqlc.arg(site_ids)::text[], sqlc.arg(min_severity), sqlc.arg(enabled), sqlc.arg(created_by)
)
RETURNING *;

-- name: NotifyChannelGet :one
SELECT * FROM notification_channels WHERE id = sqlc.arg(id);

-- name: NotifyChannelGetForUpdate :one
SELECT * FROM notification_channels WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: NotifyChannelUpdate :one
UPDATE notification_channels
SET name = sqlc.arg(name),
    config_ciphertext = sqlc.arg(config_ciphertext),
    config_wrapped_dek = sqlc.arg(config_wrapped_dek),
    config_kek_id = sqlc.arg(config_kek_id),
    event_types = sqlc.arg(event_types)::text[],
    site_ids = sqlc.arg(site_ids)::text[],
    min_severity = sqlc.arg(min_severity),
    enabled = sqlc.arg(enabled),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: NotifyChannelDelete :one
DELETE FROM notification_channels WHERE id = sqlc.arg(id) RETURNING *;

-- NotifyChannelList pages the channels of a tenant ordered by name. Tenant-wide
-- channels are included when include_tenant is true; namespace channels when
-- their namespace is listed in namespace_ids.
-- name: NotifyChannelList :many
SELECT * FROM notification_channels
WHERE tenant_id = sqlc.arg(tenant_id)
  AND ((namespace_id IS NULL AND sqlc.arg(include_tenant)::boolean)
       OR namespace_id = ANY(sqlc.arg(namespace_ids)::text[]))
  AND name > sqlc.arg(after_name)
ORDER BY name
LIMIT sqlc.arg(limit_rows);

-- name: NotifyChannelCount :one
SELECT count(*) FROM notification_channels
WHERE tenant_id = sqlc.arg(tenant_id)
  AND ((namespace_id IS NULL AND sqlc.arg(include_tenant)::boolean)
       OR namespace_id = ANY(sqlc.arg(namespace_ids)::text[]));

-- NotifyChannelListEnabled returns the enabled channels of a tenant, bounded
-- by limit_rows, for alert matching.
-- name: NotifyChannelListEnabled :many
SELECT * FROM notification_channels
WHERE tenant_id = sqlc.arg(tenant_id) AND enabled
ORDER BY id
LIMIT sqlc.arg(limit_rows);

-- name: NotifyChannelSetDelivery :exec
UPDATE notification_channels
SET last_delivery_at = sqlc.arg(delivered_at),
    last_delivery_status = sqlc.arg(status)
WHERE id = sqlc.arg(id);

-- NotifyTenantsSubscribed lists tenants having at least one enabled channel
-- subscribed to an alert kind (an empty subscription list means every kind).
-- name: NotifyTenantsSubscribed :many
SELECT DISTINCT tenant_id FROM notification_channels
WHERE enabled
  AND (cardinality(event_types) = 0 OR sqlc.arg(kind)::text = ANY(event_types))
ORDER BY tenant_id
LIMIT sqlc.arg(limit_rows);
