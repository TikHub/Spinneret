-- AccountInsertRefs creates the accounts that do not exist yet and returns
-- only the created rows.
-- name: AccountInsertRefs :many
INSERT INTO accounts (id, site_id, external_ref)
SELECT u.id, sqlc.arg(site_id)::text, u.external_ref
FROM (SELECT unnest(sqlc.arg(ids)::text[]) AS id, unnest(sqlc.arg(refs)::text[]) AS external_ref) u
ON CONFLICT (site_id, external_ref) DO NOTHING
RETURNING id, external_ref;

-- name: AccountIDsByRefs :many
SELECT id, external_ref
FROM accounts
WHERE site_id = sqlc.arg(site_id)
  AND external_ref = ANY (sqlc.arg(refs)::text[]);

-- name: AccountInsert :one
INSERT INTO accounts (id, site_id, external_ref, region, tags, notes)
VALUES (sqlc.arg(id), sqlc.arg(site_id), sqlc.arg(external_ref), sqlc.arg(region), sqlc.arg(tags), sqlc.arg(notes))
ON CONFLICT (site_id, external_ref) DO NOTHING
RETURNING id;

-- name: AccountUpdateByRef :one
UPDATE accounts
SET region = sqlc.arg(region),
    tags = sqlc.arg(tags),
    notes = sqlc.arg(notes),
    updated_at = now()
WHERE site_id = sqlc.arg(site_id)
  AND external_ref = sqlc.arg(external_ref)
RETURNING id;

-- name: AccountGet :one
SELECT a.id, a.site_id, a.external_ref, a.region, a.tags, a.state, a.ban_until, a.cooldown_until, a.notes,
       a.created_at, a.updated_at,
       (SELECT count(*) FROM identities i WHERE i.account_id = a.id)::integer AS identity_count
FROM accounts a
WHERE a.id = sqlc.arg(id);

-- name: AccountList :many
SELECT a.id, a.site_id, a.external_ref, a.region, a.tags, a.state, a.ban_until, a.cooldown_until, a.notes,
       a.created_at, a.updated_at,
       (SELECT count(*) FROM identities i WHERE i.account_id = a.id)::integer AS identity_count
FROM accounts a
WHERE a.site_id = ANY (sqlc.arg(site_ids)::text[])
  AND (sqlc.arg(search)::text = '' OR strpos(lower(a.external_ref), lower(sqlc.arg(search)::text)) > 0)
  AND (sqlc.arg(state)::text = '' OR a.state = sqlc.arg(state)::text)
  AND a.id > sqlc.arg(after_id)::text
ORDER BY a.id
LIMIT sqlc.arg(max_rows)::integer;

-- name: AccountCount :one
SELECT count(*)::bigint AS n
FROM accounts a
WHERE a.site_id = ANY (sqlc.arg(site_ids)::text[])
  AND (sqlc.arg(search)::text = '' OR strpos(lower(a.external_ref), lower(sqlc.arg(search)::text)) > 0)
  AND (sqlc.arg(state)::text = '' OR a.state = sqlc.arg(state)::text);
