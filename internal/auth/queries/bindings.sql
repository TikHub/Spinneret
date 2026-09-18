-- Role binding, tenant and namespace lookups of the auth track.

-- name: AuthBindingsOfUser :many
SELECT id, user_id, tenant_id, role, namespace_id, site_ids, extra_permissions, created_by, created_at
FROM role_bindings
WHERE user_id = $1
ORDER BY created_at, id;

-- name: AuthBindingsOfUsers :many
SELECT rb.id, rb.user_id, u.username, rb.tenant_id, rb.role, rb.namespace_id, coalesce(n.name, '')::text AS namespace_name,
       rb.site_ids, rb.extra_permissions, rb.created_by, rb.created_at
FROM role_bindings rb
JOIN users u ON u.id = rb.user_id
LEFT JOIN namespaces n ON n.id = rb.namespace_id
WHERE rb.user_id = ANY(@user_ids::text[])
  AND (@tenant_id::text = '' OR rb.tenant_id = @tenant_id::text)
ORDER BY rb.tenant_id, rb.created_at, rb.id;

-- name: AuthBindingList :many
SELECT rb.id, rb.user_id, u.username, rb.tenant_id, rb.role, rb.namespace_id, coalesce(n.name, '')::text AS namespace_name,
       rb.site_ids, rb.extra_permissions, rb.created_by, rb.created_at
FROM role_bindings rb
JOIN users u ON u.id = rb.user_id
LEFT JOIN namespaces n ON n.id = rb.namespace_id
WHERE rb.tenant_id = @tenant_id::text
  AND (@user_id::text = '' OR rb.user_id = @user_id::text)
  AND (NOT @has_cursor::boolean
       OR (u.username, rb.created_at, rb.id) > (@cursor_username::text, @cursor_created_at::timestamptz, @cursor_id::text))
ORDER BY u.username, rb.created_at, rb.id
LIMIT @page_limit;

-- name: AuthBindingCount :one
SELECT count(*)::integer AS total
FROM role_bindings rb
WHERE rb.tenant_id = @tenant_id::text
  AND (@user_id::text = '' OR rb.user_id = @user_id::text);

-- name: AuthBindingGet :one
SELECT rb.id, rb.user_id, u.username, rb.tenant_id, rb.role, rb.namespace_id, coalesce(n.name, '')::text AS namespace_name,
       rb.site_ids, rb.extra_permissions, rb.created_by, rb.created_at
FROM role_bindings rb
JOIN users u ON u.id = rb.user_id
LEFT JOIN namespaces n ON n.id = rb.namespace_id
WHERE rb.id = $1;

-- name: AuthBindingInsert :one
INSERT INTO role_bindings (id, user_id, tenant_id, role, namespace_id, site_ids, extra_permissions, created_by)
VALUES (@id, @user_id, @tenant_id, @role, sqlc.narg('namespace_id'), @site_ids, @extra_permissions, @created_by)
RETURNING id, user_id, tenant_id, role, namespace_id, site_ids, extra_permissions, created_by, created_at;

-- name: AuthBindingDelete :execrows
DELETE FROM role_bindings
WHERE id = $1;

-- name: AuthTenantOwnerBindingsForUpdate :many
SELECT id
FROM role_bindings
WHERE tenant_id = $1
  AND role = 'owner'
  AND namespace_id IS NULL
  AND cardinality(site_ids) = 0
ORDER BY id
FOR UPDATE;

-- name: AuthSitesByIDs :many
SELECT id, namespace_id, name
FROM sites
WHERE id = ANY(@ids::text[]);

-- name: AuthTenantsAll :many
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
ORDER BY name
LIMIT @page_limit;

-- name: AuthTenantsByIDs :many
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
WHERE id = ANY(@ids::text[])
ORDER BY name;

-- name: AuthTenantExists :one
SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1) AS present;

-- name: AuthNamespacesOfTenants :many
SELECT id, tenant_id, name, display_name, description, created_at, updated_at
FROM namespaces
WHERE tenant_id = ANY(@tenant_ids::text[])
ORDER BY tenant_id, name;
