-- System keys (vault-secrets track).

-- name: VaultSystemKeyGet :one
SELECT name, ciphertext, wrapped_dek, kek_id
FROM system_keys
WHERE name = sqlc.arg(name);

-- VaultSystemKeyInsert inserts a key unless one with the same name exists; it
-- returns 0 affected rows when another instance won the race.
-- name: VaultSystemKeyInsert :execrows
INSERT INTO system_keys (name, ciphertext, wrapped_dek, kek_id)
VALUES (sqlc.arg(name), sqlc.arg(ciphertext), sqlc.arg(wrapped_dek), sqlc.arg(kek_id))
ON CONFLICT (name) DO NOTHING;
