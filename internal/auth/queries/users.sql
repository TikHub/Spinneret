-- User and session-related queries of the auth track.

-- name: AuthUserByUsername :one
SELECT id, username, display_name, email, password_hash, is_platform_admin, disabled, locale, last_login_at,
       last_login_ip, password_changed_at, created_at, updated_at
FROM users
WHERE username = $1;

-- name: AuthUserByID :one
SELECT id, username, display_name, email, password_hash, is_platform_admin, disabled, locale, last_login_at,
       last_login_ip, password_changed_at, created_at, updated_at
FROM users
WHERE id = $1;

-- name: AuthUserInsert :one
INSERT INTO users (id, username, display_name, email, password_hash, is_platform_admin, password_changed_at)
VALUES (@id, @username, @display_name, @email, @password_hash, @is_platform_admin, now())
RETURNING id, username, display_name, email, password_hash, is_platform_admin, disabled, locale, last_login_at,
          last_login_ip, password_changed_at, created_at, updated_at;

-- name: AuthUserRecordLogin :exec
UPDATE users
SET last_login_at = now(),
    last_login_ip = @ip
WHERE id = @id;

-- AuthUserSetPassword returns the new password generation (sessions store the
-- generation they were created with, see sessions.go).
-- name: AuthUserSetPassword :one
UPDATE users
SET password_hash = @password_hash,
    password_changed_at = now(),
    updated_at = now()
WHERE id = @id
RETURNING password_changed_at;

-- name: AuthUserUpdate :one
UPDATE users
SET display_name = coalesce(sqlc.narg('display_name')::text, display_name),
    email = coalesce(sqlc.narg('email')::text, email),
    locale = coalesce(sqlc.narg('locale')::text, locale),
    disabled = coalesce(sqlc.narg('disabled')::boolean, disabled),
    updated_at = now()
WHERE id = @id
RETURNING id, username, display_name, email, password_hash, is_platform_admin, disabled, locale, last_login_at,
          last_login_ip, password_changed_at, created_at, updated_at;

-- name: AuthUserListMembers :many
SELECT u.id, u.username, u.display_name, u.email, u.is_platform_admin, u.disabled, u.locale, u.last_login_at,
       u.created_at
FROM users u
WHERE (@all_users::boolean OR EXISTS (
        SELECT 1 FROM role_bindings rb WHERE rb.user_id = u.id AND rb.tenant_id = @tenant_id::text))
  AND (@query::text = ''
       OR strpos(lower(u.username), lower(@query::text)) > 0
       OR strpos(lower(u.display_name), lower(@query::text)) > 0
       OR strpos(lower(u.email), lower(@query::text)) > 0)
  AND u.username > @after_username::text
ORDER BY u.username
LIMIT @page_limit;

-- name: AuthUserCountMembers :one
SELECT count(*)::integer AS total
FROM users u
WHERE (@all_users::boolean OR EXISTS (
        SELECT 1 FROM role_bindings rb WHERE rb.user_id = u.id AND rb.tenant_id = @tenant_id::text))
  AND (@query::text = ''
       OR strpos(lower(u.username), lower(@query::text)) > 0
       OR strpos(lower(u.display_name), lower(@query::text)) > 0
       OR strpos(lower(u.email), lower(@query::text)) > 0);

-- name: AuthPlatformAdminExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE is_platform_admin) AS present;

-- name: AuthBootstrapLock :exec
SELECT pg_advisory_xact_lock(hashtext('spinneret:bootstrap-platform-admin'));

-- name: AuthTenantByName :one
SELECT id, name, display_name, description, created_at, updated_at
FROM tenants
WHERE name = $1;

-- name: AuthTenantInsert :one
INSERT INTO tenants (id, name, display_name)
VALUES (@id, @name, @display_name)
RETURNING id, name, display_name, description, created_at, updated_at;

-- name: AuthNamespaceByName :one
SELECT id, tenant_id, name
FROM namespaces
WHERE tenant_id = @tenant_id AND name = @name;

-- name: AuthNamespaceInsert :one
INSERT INTO namespaces (id, tenant_id, name, display_name)
VALUES (@id, @tenant_id, @name, @display_name)
RETURNING id, tenant_id, name;
