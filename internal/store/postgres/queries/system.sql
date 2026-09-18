-- Ping is a trivial round trip used by readiness checks.
-- name: Ping :one
SELECT 1::integer AS ok;

-- name: GetSystemSetting :one
SELECT key, value, updated_at
FROM system_settings
WHERE key = sqlc.arg(key);

-- name: UpsertSystemSetting :one
INSERT INTO system_settings (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), now())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = EXCLUDED.updated_at
RETURNING key, value, updated_at;

-- name: GetSystemKey :one
SELECT name, ciphertext, wrapped_dek, kek_id, created_at, updated_at
FROM system_keys
WHERE name = sqlc.arg(name);

-- InsertSystemKeyIfAbsent returns pgx.ErrNoRows when a key with the same name
-- already exists; callers then load the existing key with GetSystemKey.
-- name: InsertSystemKeyIfAbsent :one
INSERT INTO system_keys (name, ciphertext, wrapped_dek, kek_id)
VALUES (sqlc.arg(name), sqlc.arg(ciphertext), sqlc.arg(wrapped_dek), sqlc.arg(kek_id))
ON CONFLICT (name) DO NOTHING
RETURNING name, ciphertext, wrapped_dek, kek_id, created_at, updated_at;

-- name: ListSystemKeysByKEK :many
SELECT name, ciphertext, wrapped_dek, kek_id, created_at, updated_at
FROM system_keys
WHERE kek_id = sqlc.arg(kek_id)
ORDER BY name;

-- name: ListSystemKeysNotOnKEK :many
SELECT name, ciphertext, wrapped_dek, kek_id, created_at, updated_at
FROM system_keys
WHERE kek_id <> sqlc.arg(kek_id)
ORDER BY name;

-- UpdateSystemKeyWrap replaces the wrapped DEK of a key only while it is still
-- wrapped by old_kek_id (compare-and-swap), so concurrent rewraps cannot
-- overwrite each other. It returns the number of updated rows (0 or 1).
-- name: UpdateSystemKeyWrap :execrows
UPDATE system_keys
SET wrapped_dek = sqlc.arg(wrapped_dek),
    kek_id = sqlc.arg(kek_id),
    updated_at = now()
WHERE name = sqlc.arg(name)
  AND kek_id = sqlc.arg(old_kek_id);
