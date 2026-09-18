-- name: StateEventsRecentBySubject :many
SELECT id, created_at, site_id, subject_kind, subject_id, endpoint_group_id, from_state, to_state, action, scope,
       until, permanent, outcome, policy_id, policy_version, rule, report_id, lease_id, actor, reason, shadow
FROM state_events
WHERE namespace_id = sqlc.arg(namespace_id)
  AND subject_kind = sqlc.arg(subject_kind)
  AND subject_id = sqlc.arg(subject_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(max_rows)::integer;

-- name: StateEventCopy :copyfrom
INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
                          from_state, to_state, action, actor, reason)
VALUES (sqlc.arg(id), sqlc.arg(created_at), sqlc.arg(tenant_id), sqlc.arg(namespace_id), sqlc.arg(site_id),
        sqlc.arg(subject_kind), sqlc.arg(subject_id), sqlc.arg(from_state), sqlc.arg(to_state), sqlc.arg(action),
        sqlc.arg(actor), sqlc.arg(reason));
