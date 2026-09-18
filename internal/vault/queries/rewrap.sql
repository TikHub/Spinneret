-- KEK rewrap job and status (vault-secrets track).

-- name: VaultSettingGet :one
SELECT value, updated_at
FROM system_settings
WHERE key = sqlc.arg(key);

-- name: VaultSettingPut :exec
INSERT INTO system_settings (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), now())
ON CONFLICT (key) DO UPDATE
SET value      = EXCLUDED.value,
    updated_at = EXCLUDED.updated_at;

-- VaultKEKRecordCounts counts envelope-encrypted records per KEK id across
-- every table holding wrapped DEKs.
-- name: VaultKEKRecordCounts :many
SELECT t.kek_id::text AS kek_id, count(*)::bigint AS records
FROM (
    SELECT kek_id FROM identity_payloads
    UNION ALL
    SELECT kek_id FROM secret_versions
    UNION ALL
    SELECT url_kek_id FROM proxies
    UNION ALL
    SELECT config_kek_id FROM notification_channels
    UNION ALL
    SELECT kek_id FROM system_keys
) AS t
GROUP BY t.kek_id
ORDER BY t.kek_id;

-- name: VaultRewrapPendingCount :one
SELECT ((SELECT count(*) FROM identity_payloads WHERE identity_payloads.kek_id <> sqlc.arg(current_kek_id)::text)
      + (SELECT count(*) FROM secret_versions WHERE secret_versions.kek_id <> sqlc.arg(current_kek_id)::text)
      + (SELECT count(*) FROM proxies WHERE proxies.url_kek_id <> sqlc.arg(current_kek_id)::text)
      + (SELECT count(*) FROM notification_channels WHERE notification_channels.config_kek_id <> sqlc.arg(current_kek_id)::text)
      + (SELECT count(*) FROM system_keys WHERE system_keys.kek_id <> sqlc.arg(current_kek_id)::text))::bigint AS pending;

-- name: VaultRewrapIdentityPayloadBatch :many
SELECT identity_id, version, wrapped_dek, kek_id
FROM identity_payloads
WHERE kek_id <> sqlc.arg(current_kek_id)::text
  AND (identity_id, version) > (sqlc.arg(after_id)::text, sqlc.arg(after_version)::integer)
ORDER BY identity_id, version
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultRewrapIdentityPayloadUpdate :execrows
UPDATE identity_payloads AS t
SET wrapped_dek = u.new_wrapped_dek,
    kek_id      = sqlc.arg(new_kek_id)::text
FROM (
    SELECT unnest(sqlc.arg(ids)::text[])                AS id,
           unnest(sqlc.arg(versions)::integer[])        AS version,
           unnest(sqlc.arg(old_wrapped_deks)::bytea[])  AS old_wrapped_dek,
           unnest(sqlc.arg(old_kek_ids)::text[])        AS old_kek_id,
           unnest(sqlc.arg(new_wrapped_deks)::bytea[])  AS new_wrapped_dek
) AS u
WHERE t.identity_id = u.id
  AND t.version = u.version
  AND t.kek_id = u.old_kek_id
  AND t.wrapped_dek = u.old_wrapped_dek;

-- name: VaultRewrapSecretVersionBatch :many
SELECT secret_id, version, wrapped_dek, kek_id
FROM secret_versions
WHERE kek_id <> sqlc.arg(current_kek_id)::text
  AND (secret_id, version) > (sqlc.arg(after_id)::text, sqlc.arg(after_version)::integer)
ORDER BY secret_id, version
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultRewrapSecretVersionUpdate :execrows
UPDATE secret_versions AS t
SET wrapped_dek = u.new_wrapped_dek,
    kek_id      = sqlc.arg(new_kek_id)::text
FROM (
    SELECT unnest(sqlc.arg(ids)::text[])                AS id,
           unnest(sqlc.arg(versions)::integer[])        AS version,
           unnest(sqlc.arg(old_wrapped_deks)::bytea[])  AS old_wrapped_dek,
           unnest(sqlc.arg(old_kek_ids)::text[])        AS old_kek_id,
           unnest(sqlc.arg(new_wrapped_deks)::bytea[])  AS new_wrapped_dek
) AS u
WHERE t.secret_id = u.id
  AND t.version = u.version
  AND t.kek_id = u.old_kek_id
  AND t.wrapped_dek = u.old_wrapped_dek;

-- name: VaultRewrapProxyBatch :many
SELECT id, url_wrapped_dek AS wrapped_dek, url_kek_id AS kek_id
FROM proxies
WHERE url_kek_id <> sqlc.arg(current_kek_id)::text
  AND id > sqlc.arg(after_id)::text
ORDER BY id
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultRewrapProxyUpdate :execrows
UPDATE proxies AS t
SET url_wrapped_dek = u.new_wrapped_dek,
    url_kek_id      = sqlc.arg(new_kek_id)::text
FROM (
    SELECT unnest(sqlc.arg(ids)::text[])                AS id,
           unnest(sqlc.arg(old_wrapped_deks)::bytea[])  AS old_wrapped_dek,
           unnest(sqlc.arg(old_kek_ids)::text[])        AS old_kek_id,
           unnest(sqlc.arg(new_wrapped_deks)::bytea[])  AS new_wrapped_dek
) AS u
WHERE t.id = u.id
  AND t.url_kek_id = u.old_kek_id
  AND t.url_wrapped_dek = u.old_wrapped_dek;

-- name: VaultRewrapChannelBatch :many
SELECT id, config_wrapped_dek AS wrapped_dek, config_kek_id AS kek_id
FROM notification_channels
WHERE config_kek_id <> sqlc.arg(current_kek_id)::text
  AND id > sqlc.arg(after_id)::text
ORDER BY id
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultRewrapChannelUpdate :execrows
UPDATE notification_channels AS t
SET config_wrapped_dek = u.new_wrapped_dek,
    config_kek_id      = sqlc.arg(new_kek_id)::text
FROM (
    SELECT unnest(sqlc.arg(ids)::text[])                AS id,
           unnest(sqlc.arg(old_wrapped_deks)::bytea[])  AS old_wrapped_dek,
           unnest(sqlc.arg(old_kek_ids)::text[])        AS old_kek_id,
           unnest(sqlc.arg(new_wrapped_deks)::bytea[])  AS new_wrapped_dek
) AS u
WHERE t.id = u.id
  AND t.config_kek_id = u.old_kek_id
  AND t.config_wrapped_dek = u.old_wrapped_dek;

-- name: VaultRewrapSystemKeyBatch :many
SELECT name AS id, wrapped_dek, kek_id
FROM system_keys
WHERE kek_id <> sqlc.arg(current_kek_id)::text
  AND name > sqlc.arg(after_id)::text
ORDER BY name
LIMIT sqlc.arg(limit_rows)::integer;

-- name: VaultRewrapSystemKeyUpdate :execrows
UPDATE system_keys AS t
SET wrapped_dek = u.new_wrapped_dek,
    kek_id      = sqlc.arg(new_kek_id)::text,
    updated_at  = now()
FROM (
    SELECT unnest(sqlc.arg(ids)::text[])                AS id,
           unnest(sqlc.arg(old_wrapped_deks)::bytea[])  AS old_wrapped_dek,
           unnest(sqlc.arg(old_kek_ids)::text[])        AS old_kek_id,
           unnest(sqlc.arg(new_wrapped_deks)::bytea[])  AS new_wrapped_dek
) AS u
WHERE t.name = u.id
  AND t.kek_id = u.old_kek_id
  AND t.wrapped_dek = u.old_wrapped_dek;
