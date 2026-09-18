-- Tenant and namespace queries of the tenancy service.

-- name: TenancyTenantList :many
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
WHERE (@all_tenants::boolean OR id = ANY(@ids::text[]))
  AND name > @after_name::text
ORDER BY name
LIMIT @page_limit;

-- name: TenancyTenantCount :one
SELECT count(*)::integer AS total
FROM tenants
WHERE (@all_tenants::boolean OR id = ANY(@ids::text[]));

-- name: TenancyTenantGet :one
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
WHERE id = $1;

-- name: TenancyTenantLock :one
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
WHERE id = $1
FOR UPDATE;

-- name: TenancyTenantInsert :one
INSERT INTO tenants (id, name, display_name, description)
VALUES (@id, @name, @display_name, @description)
RETURNING id, name, display_name, description, created_at, updated_at;

-- name: TenancyTenantUpdate :one
UPDATE tenants
SET display_name = coalesce(sqlc.narg('display_name')::text, display_name),
    description = coalesce(sqlc.narg('description')::text, description),
    updated_at = now()
WHERE id = @id
RETURNING id, name, display_name, description, created_at, updated_at;

-- name: TenancyTenantDelete :execrows
DELETE FROM tenants
WHERE id = $1;

-- name: TenancyTenantNamespaceCount :one
SELECT count(*)::integer AS total
FROM namespaces
WHERE tenant_id = $1;

-- name: TenancyNamespacesOfTenant :many
SELECT id, tenant_id, name, display_name, description, created_at, updated_at
FROM namespaces
WHERE tenant_id = $1
ORDER BY name
LIMIT @page_limit;

-- name: TenancyNamespaceGet :one
SELECT id, tenant_id, name, display_name, description, created_at, updated_at
FROM namespaces
WHERE id = $1;

-- name: TenancyNamespaceLock :one
SELECT id, tenant_id, name, display_name, description, created_at, updated_at
FROM namespaces
WHERE id = $1
FOR UPDATE;

-- name: TenancyNamespaceInsert :one
INSERT INTO namespaces (id, tenant_id, name, display_name, description)
VALUES (@id, @tenant_id, @name, @display_name, @description)
RETURNING id, tenant_id, name, display_name, description, created_at, updated_at;

-- name: TenancyNamespaceUpdate :one
UPDATE namespaces
SET display_name = coalesce(sqlc.narg('display_name')::text, display_name),
    description = coalesce(sqlc.narg('description')::text, description),
    updated_at = now()
WHERE id = @id
RETURNING id, tenant_id, name, display_name, description, created_at, updated_at;

-- name: TenancyNamespaceDelete :execrows
DELETE FROM namespaces
WHERE id = $1;

-- name: TenancyNamespaceUsage :one
SELECT (SELECT count(*) FROM sites s WHERE s.namespace_id = @namespace_id::text)::integer AS sites,
       (SELECT count(*) FROM proxies p WHERE p.namespace_id = @namespace_id::text)::integer AS proxies,
       (SELECT count(*) FROM config_items c WHERE c.namespace_id = @namespace_id::text)::integer AS config_items,
       (SELECT count(*) FROM secrets x WHERE x.namespace_id = @namespace_id::text)::integer AS secrets,
       -- Revoked and expired tokens can never authenticate again and are deleted with the namespace (there is
       -- no API to delete them), so only usable tokens block the deletion.
       (SELECT count(*) FROM api_tokens t
        WHERE t.namespace_id = @namespace_id::text
          AND t.revoked_at IS NULL
          AND (t.expires_at IS NULL OR t.expires_at > now()))::integer AS tokens;

-- name: TenancyUserExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1) AS present;

-- name: TenancyOwnerBindingInsert :exec
INSERT INTO role_bindings (id, user_id, tenant_id, role, created_by)
VALUES (@id, @user_id, @tenant_id, 'owner', @created_by);
