-- Audit log reads of the auth track (writes are done by internal/audit).

-- name: AuthAuditList :many
SELECT id, created_at, tenant_id, namespace_id, actor_kind, actor_id, actor_name, action, resource_kind, resource_id,
       resource_name, result, ip, user_agent, details
FROM audit_logs
WHERE tenant_id = @tenant_id::text
  AND created_at >= @start_at::timestamptz
  AND created_at < @end_at::timestamptz
  AND (@namespace_id::text = '' OR namespace_id = @namespace_id::text)
  AND (@all_namespaces::boolean OR namespace_id = ANY(@namespace_ids::text[]))
  AND (@actor::text = '' OR actor_id = @actor::text OR actor_name = @actor::text)
  AND (@action::text = '' OR action = @action::text)
  AND (@resource_kind::text = '' OR resource_kind = @resource_kind::text)
  AND (@resource_id::text = '' OR resource_id = @resource_id::text)
  AND (@result::text = '' OR result = @result::text)
  AND (NOT @has_cursor::boolean OR (created_at, id) < (@cursor_created_at::timestamptz, @cursor_id::text))
ORDER BY created_at DESC, id DESC
LIMIT @page_limit;

-- name: AuthNamespaceNames :many
SELECT id, name
FROM namespaces
WHERE id = ANY(@ids::text[]);
