-- Secondary indexes for lookups found missing after the first service pass,
-- plus a stricter secret path constraint.
--
-- Indexes on the range-partitioned parents (audit_logs, risk_events,
-- state_events) are partitioned indexes: PostgreSQL creates the matching index
-- on every existing partition and on every partition attached later by
-- spinneret_ensure_partitions.

-- +goose Up

-- Audit trail of one resource (vault secret history, resource audit tabs).
CREATE INDEX audit_logs_tenant_id_resource_kind_resource_id_created_at_idx
    ON audit_logs (tenant_id, resource_kind, resource_id, created_at DESC);

-- KEK rotation: find rows still encrypted under a given key.
CREATE INDEX proxies_url_kek_id_idx ON proxies (url_kek_id);
CREATE INDEX notification_channels_config_kek_id_idx ON notification_channels (config_kek_id);

-- Keyset pagination of the identities of one site and client.
CREATE INDEX identities_site_id_client_id_idx ON identities (site_id, client, id);

-- Risk event history of one identity or one proxy.
CREATE INDEX risk_events_identity_id_created_at_idx ON risk_events (identity_id, created_at DESC);
CREATE INDEX risk_events_proxy_id_created_at_idx ON risk_events (proxy_id, created_at DESC);

-- State events filtered by action (bans, quarantines, manual operations).
CREATE INDEX state_events_action_created_at_idx ON state_events (action, created_at DESC);

-- Alert retention purges by age.
CREATE INDEX alert_events_created_at_idx ON alert_events (created_at);

-- Secret paths are "/"-separated segments: no empty segment (so no leading,
-- trailing or doubled "/") and no "." or ".." segment. This mirrors
-- vault.ValidateSecretPath; secrets_path_check keeps the character set and
-- the length bound.
ALTER TABLE secrets ADD CONSTRAINT secrets_path_segments_check CHECK (
    path <> '' AND path !~ '(^/|/$|//|(^|/)\.\.?(/|$))'
);

-- +goose Down

ALTER TABLE secrets DROP CONSTRAINT IF EXISTS secrets_path_segments_check;

DROP INDEX IF EXISTS alert_events_created_at_idx;
DROP INDEX IF EXISTS state_events_action_created_at_idx;
DROP INDEX IF EXISTS risk_events_proxy_id_created_at_idx;
DROP INDEX IF EXISTS risk_events_identity_id_created_at_idx;
DROP INDEX IF EXISTS identities_site_id_client_id_idx;
DROP INDEX IF EXISTS notification_channels_config_kek_id_idx;
DROP INDEX IF EXISTS proxies_url_kek_id_idx;
DROP INDEX IF EXISTS audit_logs_tenant_id_resource_kind_resource_id_created_at_idx;
