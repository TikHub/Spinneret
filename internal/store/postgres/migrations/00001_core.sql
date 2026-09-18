-- Spinneret core schema: tenancy, access control, sites, identities, proxies,
-- policies, hot-state snapshots, breaker history, config center, vault,
-- notifications and system settings. Column lists follow section 4 of
-- the implementation specification.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Tenancy
-- ---------------------------------------------------------------------------

CREATE TABLE tenants (
    id           text        PRIMARY KEY,
    name         text        NOT NULL,
    display_name text        NOT NULL DEFAULT '',
    description  text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenants_name_key UNIQUE (name)
);

CREATE TABLE namespaces (
    id           text        PRIMARY KEY,
    tenant_id    text        NOT NULL REFERENCES tenants (id),
    name         text        NOT NULL,
    display_name text        NOT NULL DEFAULT '',
    description  text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT namespaces_tenant_id_name_key UNIQUE (tenant_id, name)
);

-- ---------------------------------------------------------------------------
-- Users, role bindings and API tokens
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id                  text        PRIMARY KEY,
    username            text        NOT NULL,
    display_name        text        NOT NULL DEFAULT '',
    email               text        NOT NULL DEFAULT '',
    password_hash       text        NOT NULL,
    is_platform_admin   boolean     NOT NULL DEFAULT false,
    disabled            boolean     NOT NULL DEFAULT false,
    locale              text        NOT NULL DEFAULT '',
    last_login_at       timestamptz,
    last_login_ip       text        NOT NULL DEFAULT '',
    password_changed_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_username_key UNIQUE (username),
    CONSTRAINT users_username_lower_check CHECK (username = lower(username))
);

CREATE TABLE role_bindings (
    id                text        PRIMARY KEY,
    user_id           text        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id         text        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    role              text        NOT NULL,
    namespace_id      text        REFERENCES namespaces (id) ON DELETE CASCADE,
    site_ids          text[]      NOT NULL DEFAULT '{}',
    extra_permissions text[]      NOT NULL DEFAULT '{}',
    created_by        text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT role_bindings_role_check CHECK (role IN ('owner', 'admin', 'operator', 'viewer'))
);

CREATE INDEX role_bindings_user_id_idx ON role_bindings (user_id);
CREATE INDEX role_bindings_tenant_id_idx ON role_bindings (tenant_id);
CREATE INDEX role_bindings_namespace_id_idx ON role_bindings (namespace_id) WHERE namespace_id IS NOT NULL;

CREATE TABLE api_tokens (
    id             text        PRIMARY KEY,
    tenant_id      text        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    namespace_id   text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    name           text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    token_prefix   text        NOT NULL,
    token_hash     bytea       NOT NULL,
    scopes         text[]      NOT NULL DEFAULT '{}',
    ip_allowlist   text[]      NOT NULL DEFAULT '{}',
    rate_limit_rps integer     NOT NULL DEFAULT 0,
    expires_at     timestamptz,
    revoked_at     timestamptz,
    last_used_at   timestamptz,
    last_used_ip   text        NOT NULL DEFAULT '',
    created_by     text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_tokens_token_hash_key UNIQUE (token_hash),
    -- The unique index also serves lookups by namespace_id (leading column).
    CONSTRAINT api_tokens_namespace_id_name_key UNIQUE (namespace_id, name),
    CONSTRAINT api_tokens_rate_limit_rps_check CHECK (rate_limit_rps >= 0)
);

CREATE INDEX api_tokens_tenant_id_idx ON api_tokens (tenant_id);

-- ---------------------------------------------------------------------------
-- Sites, endpoint groups and URI rules
-- ---------------------------------------------------------------------------

CREATE TABLE sites (
    id            text        PRIMARY KEY,
    hkey          bigint      NOT NULL GENERATED ALWAYS AS IDENTITY,
    namespace_id  text        NOT NULL REFERENCES namespaces (id),
    name          text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    description   text        NOT NULL DEFAULT '',
    clients       text[]      NOT NULL DEFAULT '{web}',
    paused        boolean     NOT NULL DEFAULT false,
    paused_reason text        NOT NULL DEFAULT '',
    paused_at     timestamptz,
    paused_by     text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sites_hkey_key UNIQUE (hkey),
    CONSTRAINT sites_namespace_id_name_key UNIQUE (namespace_id, name)
);

CREATE TABLE endpoint_groups (
    id            text        PRIMARY KEY,
    hkey          bigint      NOT NULL GENERATED ALWAYS AS IDENTITY,
    site_id       text        NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    client        text        NOT NULL,
    name          text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    low_watermark integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT endpoint_groups_hkey_key UNIQUE (hkey),
    CONSTRAINT endpoint_groups_site_id_client_name_key UNIQUE (site_id, client, name),
    CONSTRAINT endpoint_groups_low_watermark_check CHECK (low_watermark >= 0)
);

CREATE TABLE uri_rules (
    id                text        PRIMARY KEY,
    endpoint_group_id text        NOT NULL REFERENCES endpoint_groups (id) ON DELETE CASCADE,
    kind              text        NOT NULL,
    pattern           text        NOT NULL,
    position          integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uri_rules_kind_check CHECK (kind IN ('exact', 'template', 'prefix', 'regex'))
);

CREATE INDEX uri_rules_endpoint_group_id_position_idx ON uri_rules (endpoint_group_id, position);

-- ---------------------------------------------------------------------------
-- Identity types, accounts, identities and encrypted payloads
-- ---------------------------------------------------------------------------

CREATE TABLE identity_types (
    id          text        PRIMARY KEY,
    site_id     text        NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    client      text        NOT NULL,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    spec        jsonb       NOT NULL DEFAULT '{}',
    spec_yaml   text        NOT NULL DEFAULT '',
    json_schema jsonb       NOT NULL DEFAULT '{}',
    version     integer     NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT identity_types_site_id_name_key UNIQUE (site_id, name),
    CONSTRAINT identity_types_version_check CHECK (version >= 1)
);

CREATE TABLE accounts (
    id             text        PRIMARY KEY,
    hkey           bigint      NOT NULL GENERATED ALWAYS AS IDENTITY,
    site_id        text        NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    external_ref   text        NOT NULL,
    region         text        NOT NULL DEFAULT '',
    tags           text[]      NOT NULL DEFAULT '{}',
    state          text        NOT NULL DEFAULT 'active',
    ban_until      timestamptz,
    cooldown_until timestamptz,
    notes          text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT accounts_hkey_key UNIQUE (hkey),
    CONSTRAINT accounts_site_id_external_ref_key UNIQUE (site_id, external_ref),
    CONSTRAINT accounts_state_check CHECK (state IN ('active', 'banned', 'disabled'))
);

CREATE TABLE identities (
    id               text        PRIMARY KEY,
    hkey             bigint      NOT NULL GENERATED ALWAYS AS IDENTITY,
    site_id          text        NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    client           text        NOT NULL,
    type_id          text        NOT NULL REFERENCES identity_types (id),
    account_id       text        REFERENCES accounts (id) ON DELETE SET NULL,
    state            text        NOT NULL DEFAULT 'pending',
    state_reason     text        NOT NULL DEFAULT '',
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    -- ban_until IS NULL while state = 'banned' means a permanent ban.
    ban_until        timestamptz,
    quarantine_until timestamptz,
    region           text        NOT NULL DEFAULT '',
    tags             text[]      NOT NULL DEFAULT '{}',
    labels           jsonb       NOT NULL DEFAULT '{}',
    unique_hash      bytea       NOT NULL,
    payload_hash     bytea       NOT NULL,
    payload_version  integer     NOT NULL DEFAULT 1,
    activated_at     timestamptz,
    last_used_at     timestamptz,
    created_by       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT identities_hkey_key UNIQUE (hkey),
    -- The unique index also serves lookups by type_id (leading column).
    CONSTRAINT identities_type_id_unique_hash_key UNIQUE (type_id, unique_hash),
    CONSTRAINT identities_state_check CHECK (
        state IN ('pending', 'active', 'expired', 'banned', 'quarantined', 'disabled', 'retired')
    ),
    CONSTRAINT identities_payload_version_check CHECK (payload_version >= 1)
);

CREATE INDEX identities_site_id_state_idx ON identities (site_id, state);
CREATE INDEX identities_account_id_idx ON identities (account_id) WHERE account_id IS NOT NULL;
CREATE INDEX identities_banned_until_idx ON identities (state, ban_until) WHERE state = 'banned';
CREATE INDEX identities_quarantined_until_idx ON identities (state, quarantine_until) WHERE state = 'quarantined';
CREATE INDEX identities_tags_idx ON identities USING gin (tags);

CREATE TABLE identity_payloads (
    identity_id text        NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    version     integer     NOT NULL,
    ciphertext  bytea       NOT NULL,
    wrapped_dek bytea       NOT NULL,
    kek_id      text        NOT NULL,
    created_by  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (identity_id, version)
);

CREATE INDEX identity_payloads_kek_id_idx ON identity_payloads (kek_id);

-- ---------------------------------------------------------------------------
-- Proxies and identity bindings
-- ---------------------------------------------------------------------------

CREATE TABLE proxies (
    id                         text        PRIMARY KEY,
    hkey                       bigint      NOT NULL GENERATED ALWAYS AS IDENTITY,
    namespace_id               text        NOT NULL REFERENCES namespaces (id),
    scheme                     text        NOT NULL,
    host                       text        NOT NULL,
    port                       integer     NOT NULL,
    username_hint              text        NOT NULL DEFAULT '',
    display_url                text        NOT NULL,
    url_hash                   bytea       NOT NULL,
    url_ciphertext             bytea       NOT NULL,
    url_wrapped_dek            bytea       NOT NULL,
    url_kek_id                 text        NOT NULL,
    url_version                integer     NOT NULL DEFAULT 1,
    kind                       text        NOT NULL DEFAULT 'datacenter',
    region                     text        NOT NULL DEFAULT '',
    city                       text        NOT NULL DEFAULT '',
    provider                   text        NOT NULL DEFAULT '',
    max_concurrency            integer     NOT NULL DEFAULT 1,
    tags                       text[]      NOT NULL DEFAULT '{}',
    session_template           text        NOT NULL DEFAULT '',
    state                      text        NOT NULL DEFAULT 'active',
    state_reason               text        NOT NULL DEFAULT '',
    state_changed_at           timestamptz NOT NULL DEFAULT now(),
    ban_until                  timestamptz,
    cooldown_until             timestamptz,
    consecutive_check_failures integer     NOT NULL DEFAULT 0,
    last_check_at              timestamptz,
    last_check_ok              boolean,
    last_latency_ms            integer,
    exit_ip                    text        NOT NULL DEFAULT '',
    next_check_at              timestamptz NOT NULL DEFAULT now(),
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT proxies_hkey_key UNIQUE (hkey),
    CONSTRAINT proxies_namespace_id_url_hash_key UNIQUE (namespace_id, url_hash),
    CONSTRAINT proxies_scheme_check CHECK (scheme IN ('http', 'https', 'socks5')),
    CONSTRAINT proxies_port_check CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT proxies_kind_check CHECK (kind IN ('datacenter', 'residential', 'mobile', 'tunnel')),
    CONSTRAINT proxies_state_check CHECK (
        state IN ('active', 'disabled', 'dead', 'banned', 'quarantined', 'retired')
    ),
    CONSTRAINT proxies_max_concurrency_check CHECK (max_concurrency >= 0),
    CONSTRAINT proxies_consecutive_check_failures_check CHECK (consecutive_check_failures >= 0)
);

CREATE INDEX proxies_namespace_id_state_idx ON proxies (namespace_id, state);
CREATE INDEX proxies_next_check_at_idx ON proxies (next_check_at);
CREATE INDEX proxies_tags_idx ON proxies USING gin (tags);

CREATE TABLE proxy_bindings (
    identity_id   text        PRIMARY KEY REFERENCES identities (id) ON DELETE CASCADE,
    proxy_id      text        NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    bound_at      timestamptz NOT NULL DEFAULT now(),
    rebind_day    date        NOT NULL DEFAULT ((now() AT TIME ZONE 'UTC')::date),
    rebinds_today integer     NOT NULL DEFAULT 0,
    CONSTRAINT proxy_bindings_rebinds_today_check CHECK (rebinds_today >= 0)
);

CREATE INDEX proxy_bindings_proxy_id_idx ON proxy_bindings (proxy_id);

-- ---------------------------------------------------------------------------
-- Policies
-- ---------------------------------------------------------------------------

CREATE TABLE policies (
    id               text        PRIMARY KEY,
    namespace_id     text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    kind             text        NOT NULL,
    name             text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    current_version  integer     NOT NULL DEFAULT 0,
    -- draft_yaml IS NULL means there is no unpublished draft.
    draft_yaml       text,
    draft_updated_by text,
    draft_updated_at timestamptz,
    created_by       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT policies_namespace_id_kind_name_key UNIQUE (namespace_id, kind, name),
    CONSTRAINT policies_kind_check CHECK (kind IN ('rotation', 'signal', 'action', 'breaker')),
    CONSTRAINT policies_current_version_check CHECK (current_version >= 0)
);

CREATE TABLE policy_versions (
    policy_id  text        NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    version    integer     NOT NULL,
    spec       jsonb       NOT NULL,
    spec_yaml  text        NOT NULL,
    comment    text        NOT NULL DEFAULT '',
    created_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (policy_id, version)
);

CREATE TABLE policy_bindings (
    id                text        PRIMARY KEY,
    policy_id         text        NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    kind              text        NOT NULL,
    namespace_id      text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    site_id           text        REFERENCES sites (id) ON DELETE CASCADE,
    client            text,
    endpoint_group_id text        REFERENCES endpoint_groups (id) ON DELETE CASCADE,
    created_by        text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT policy_bindings_kind_check CHECK (kind IN ('rotation', 'signal', 'action', 'breaker')),
    -- Bindings are hierarchical: namespace > site > client > endpoint group.
    CONSTRAINT policy_bindings_client_site_check CHECK (client IS NULL OR site_id IS NOT NULL),
    CONSTRAINT policy_bindings_endpoint_group_site_check CHECK (endpoint_group_id IS NULL OR site_id IS NOT NULL)
);

CREATE UNIQUE INDEX policy_bindings_target_key ON policy_bindings (
    namespace_id, kind, coalesce(site_id, ''), coalesce(client, ''), coalesce(endpoint_group_id, '')
);
CREATE INDEX policy_bindings_policy_id_idx ON policy_bindings (policy_id);
CREATE INDEX policy_bindings_site_id_idx ON policy_bindings (site_id) WHERE site_id IS NOT NULL;
CREATE INDEX policy_bindings_endpoint_group_id_idx ON policy_bindings (endpoint_group_id)
    WHERE endpoint_group_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Hot-state snapshots and breaker history
-- ---------------------------------------------------------------------------

CREATE TABLE hot_state_snapshots (
    site_id              text             NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    subject              text             NOT NULL,
    subject_id           text             NOT NULL,
    endpoint_group_id    text             NOT NULL DEFAULT '',
    score                double precision NOT NULL DEFAULT 0,
    samples              integer          NOT NULL DEFAULT 0,
    consecutive_failures integer          NOT NULL DEFAULT 0,
    last_failure_at      timestamptz,
    cooldown_until       timestamptz,
    reuse_until          timestamptz,
    last_used_at         timestamptz,
    updated_at           timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, subject, subject_id, endpoint_group_id),
    CONSTRAINT hot_state_snapshots_subject_check CHECK (subject IN ('ie', 'ig', 'ps'))
);

CREATE TABLE breaker_events (
    id                text        PRIMARY KEY,
    created_at        timestamptz NOT NULL DEFAULT now(),
    tenant_id         text        NOT NULL,
    namespace_id      text        NOT NULL,
    site_id           text        NOT NULL,
    endpoint_group_id text        NOT NULL DEFAULT '',
    from_state        text        NOT NULL DEFAULT '',
    to_state          text        NOT NULL DEFAULT '',
    trigger           text        NOT NULL,
    reason            text        NOT NULL DEFAULT '',
    open_until        timestamptz,
    metrics           jsonb       NOT NULL DEFAULT '{}',
    actor             text        NOT NULL DEFAULT '',
    CONSTRAINT breaker_events_trigger_check CHECK (trigger IN ('auto', 'manual', 'probe', 'site_switch'))
);

CREATE INDEX breaker_events_endpoint_group_id_created_at_idx ON breaker_events (endpoint_group_id, created_at DESC);
CREATE INDEX breaker_events_namespace_id_created_at_idx ON breaker_events (namespace_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Config center
-- ---------------------------------------------------------------------------

CREATE TABLE config_items (
    id               text        PRIMARY KEY,
    namespace_id     text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    group_name       text        NOT NULL,
    key              text        NOT NULL,
    format           text        NOT NULL,
    schema           jsonb,
    description      text        NOT NULL DEFAULT '',
    current_version  integer     NOT NULL DEFAULT 0,
    -- draft_content IS NULL means there is no unpublished draft.
    draft_content    text,
    draft_updated_by text,
    draft_updated_at timestamptz,
    created_by       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT config_items_namespace_id_group_name_key_key UNIQUE (namespace_id, group_name, key),
    CONSTRAINT config_items_format_check CHECK (format IN ('json', 'yaml', 'text')),
    CONSTRAINT config_items_current_version_check CHECK (current_version >= 0)
);

CREATE TABLE config_versions (
    item_id        text        NOT NULL REFERENCES config_items (id) ON DELETE CASCADE,
    version        integer     NOT NULL,
    content        text        NOT NULL,
    comment        text        NOT NULL DEFAULT '',
    -- source_version is set when the version was produced by a rollback.
    source_version integer,
    published_by   text        NOT NULL DEFAULT '',
    published_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, version)
);

-- ---------------------------------------------------------------------------
-- Vault: secrets
-- ---------------------------------------------------------------------------

CREATE TABLE secrets (
    id               text        PRIMARY KEY,
    namespace_id     text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    path             text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    tags             text[]      NOT NULL DEFAULT '{}',
    current_version  integer     NOT NULL DEFAULT 1,
    expires_at       timestamptz,
    last_accessed_at timestamptz,
    created_by       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secrets_namespace_id_path_key UNIQUE (namespace_id, path),
    CONSTRAINT secrets_path_check CHECK (path ~ '^[a-z0-9][a-z0-9_./-]*$' AND length(path) <= 256),
    CONSTRAINT secrets_current_version_check CHECK (current_version >= 0)
);

CREATE TABLE secret_versions (
    secret_id   text        NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    version     integer     NOT NULL,
    ciphertext  bytea       NOT NULL,
    wrapped_dek bytea       NOT NULL,
    kek_id      text        NOT NULL,
    created_by  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (secret_id, version)
);

CREATE INDEX secret_versions_kek_id_idx ON secret_versions (kek_id);

-- ---------------------------------------------------------------------------
-- Notifications and alerts
-- ---------------------------------------------------------------------------

CREATE TABLE notification_channels (
    id                   text        PRIMARY KEY,
    tenant_id            text        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    namespace_id         text        REFERENCES namespaces (id) ON DELETE CASCADE,
    name                 text        NOT NULL,
    kind                 text        NOT NULL,
    config_ciphertext    bytea       NOT NULL,
    config_wrapped_dek   bytea       NOT NULL,
    config_kek_id        text        NOT NULL,
    event_types          text[]      NOT NULL DEFAULT '{}',
    site_ids             text[]      NOT NULL DEFAULT '{}',
    min_severity         text        NOT NULL DEFAULT 'warning',
    enabled              boolean     NOT NULL DEFAULT true,
    last_delivery_at     timestamptz,
    last_delivery_status text        NOT NULL DEFAULT '',
    created_by           text        NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_channels_tenant_id_name_key UNIQUE (tenant_id, name),
    CONSTRAINT notification_channels_kind_check CHECK (kind IN ('webhook', 'feishu', 'dingtalk', 'wecom', 'telegram')),
    CONSTRAINT notification_channels_min_severity_check CHECK (min_severity IN ('info', 'warning', 'critical'))
);

CREATE INDEX notification_channels_namespace_id_idx ON notification_channels (namespace_id)
    WHERE namespace_id IS NOT NULL;

CREATE TABLE alert_events (
    id           text        PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    tenant_id    text        NOT NULL,
    namespace_id text        NOT NULL DEFAULT '',
    site_id      text        NOT NULL DEFAULT '',
    kind         text        NOT NULL,
    severity     text        NOT NULL,
    title        text        NOT NULL,
    message      text        NOT NULL DEFAULT '',
    details      jsonb       NOT NULL DEFAULT '{}',
    dedup_key    text        NOT NULL DEFAULT '',
    deliveries   jsonb       NOT NULL DEFAULT '[]',
    CONSTRAINT alert_events_severity_check CHECK (severity IN ('info', 'warning', 'critical'))
);

CREATE INDEX alert_events_tenant_id_created_at_idx ON alert_events (tenant_id, created_at DESC);
CREATE INDEX alert_events_namespace_id_created_at_idx ON alert_events (namespace_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- System keys and settings
-- ---------------------------------------------------------------------------

CREATE TABLE system_keys (
    name        text        PRIMARY KEY,
    ciphertext  bytea       NOT NULL,
    wrapped_dek bytea       NOT NULL,
    kek_id      text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE system_settings (
    key        text        PRIMARY KEY,
    value      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS system_settings;
DROP TABLE IF EXISTS system_keys;
DROP TABLE IF EXISTS alert_events;
DROP TABLE IF EXISTS notification_channels;
DROP TABLE IF EXISTS secret_versions;
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS config_versions;
DROP TABLE IF EXISTS config_items;
DROP TABLE IF EXISTS breaker_events;
DROP TABLE IF EXISTS hot_state_snapshots;
DROP TABLE IF EXISTS policy_bindings;
DROP TABLE IF EXISTS policy_versions;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS proxy_bindings;
DROP TABLE IF EXISTS proxies;
DROP TABLE IF EXISTS identity_payloads;
DROP TABLE IF EXISTS identities;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS identity_types;
DROP TABLE IF EXISTS uri_rules;
DROP TABLE IF EXISTS endpoint_groups;
DROP TABLE IF EXISTS sites;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS role_bindings;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS namespaces;
DROP TABLE IF EXISTS tenants;
