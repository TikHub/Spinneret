-- API token names are unique among usable tokens only.
--
-- There is no RPC that deletes an API token: rotating one means creating a new
-- token and revoking the old one. With an unconditional UNIQUE (namespace_id,
-- name) the revoked row keeps the name forever, so a rotated token can never
-- be re-created under its own name (the create fails with already_exists) and
-- operators end up with names like "crawler-2", "crawler-3". Making the index
-- partial frees the name as soon as the token is revoked, while two usable
-- tokens still cannot share a name.
--
-- The dropped constraint also served lookups by namespace_id as the leading
-- column of its index; the partial index only covers usable tokens, so a plain
-- index on (namespace_id, name) replaces it for listings, which include
-- revoked tokens.

-- +goose Up

ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_namespace_id_name_key;

CREATE UNIQUE INDEX api_tokens_namespace_id_name_active_idx
    ON api_tokens (namespace_id, name) WHERE revoked_at IS NULL;

CREATE INDEX api_tokens_namespace_id_name_idx ON api_tokens (namespace_id, name);

-- +goose Down

DROP INDEX IF EXISTS api_tokens_namespace_id_name_idx;
DROP INDEX IF EXISTS api_tokens_namespace_id_name_active_idx;

-- The unconditional constraint needs globally unique names again: rename the
-- revoked duplicates the partial index allowed.
UPDATE api_tokens AS t
SET name = left(t.name, 32) || '-revoked-' || right(t.id, 8)
WHERE t.revoked_at IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM api_tokens AS o
      WHERE o.namespace_id = t.namespace_id AND o.name = t.name AND o.id <> t.id
  );

ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_namespace_id_name_key UNIQUE (namespace_id, name);
