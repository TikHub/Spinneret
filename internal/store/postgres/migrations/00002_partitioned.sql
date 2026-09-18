-- Spinneret partitioned history and statistics tables plus the partition
-- management functions used by the migration itself and by the
-- partition_manager background job (store/postgres.EnsurePartitions and
-- store/postgres.DropExpiredPartitions).
--
-- Partition naming: <table>_pYYYYMMDD (day) or <table>_pYYYYMM (month).
-- Partition boundaries are always aligned to UTC.

-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION spinneret_ensure_partitions(
    p_table       text,
    p_granularity text,
    p_from        timestamptz,
    p_to          timestamptz
) RETURNS integer
LANGUAGE plpgsql
SET "TimeZone" = 'UTC'
SET "DateStyle" = 'ISO, YMD'
AS $$
DECLARE
    v_parent   regclass;
    v_schema   text;
    v_relname  text;
    v_step     interval;
    v_suffix   text;
    v_start    timestamptz;
    v_end      timestamptz;
    v_name     text;
    v_existing regclass;
    v_created  integer := 0;
BEGIN
    IF p_granularity = 'day' THEN
        v_step := interval '1 day';
        v_suffix := 'YYYYMMDD';
    ELSIF p_granularity = 'month' THEN
        v_step := interval '1 month';
        v_suffix := 'YYYYMM';
    ELSE
        RAISE EXCEPTION 'spinneret_ensure_partitions: unsupported granularity "%" (want day or month)', p_granularity
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    IF p_from IS NULL OR p_to IS NULL OR p_from > p_to THEN
        RAISE EXCEPTION 'spinneret_ensure_partitions: invalid range [%, %]', p_from, p_to
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT c.oid::regclass, n.nspname, c.relname
      INTO v_parent, v_schema, v_relname
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE c.oid = to_regclass(p_table)
       AND c.relkind = 'p';

    IF v_parent IS NULL THEN
        RAISE EXCEPTION 'spinneret_ensure_partitions: "%" is not a partitioned table', p_table
            USING ERRCODE = 'undefined_table';
    END IF;

    v_start := date_trunc(p_granularity, p_from);
    WHILE v_start <= p_to LOOP
        v_end := v_start + v_step;
        v_name := v_relname || '_p' || to_char(v_start, v_suffix);

        v_existing := to_regclass(format('%I.%I', v_schema, v_name));
        IF v_existing IS NULL THEN
            BEGIN
                EXECUTE format(
                    'CREATE TABLE %I.%I PARTITION OF %s FOR VALUES FROM (%L) TO (%L)',
                    v_schema, v_name, v_parent,
                    to_char(v_start, 'YYYY-MM-DD HH24:MI:SS') || '+00',
                    to_char(v_end, 'YYYY-MM-DD HH24:MI:SS') || '+00'
                );
                v_created := v_created + 1;
            EXCEPTION
                -- Another session created the same partition concurrently;
                -- the relation it created is verified below.
                WHEN duplicate_table OR unique_violation THEN
                    v_existing := to_regclass(format('%I.%I', v_schema, v_name));
            END;
        END IF;

        -- A relation with the partition's name that is not attached to the
        -- parent (for example a detached partition) would leave the range
        -- uncovered and make inserts fail later; report it instead.
        IF v_existing IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM pg_inherits WHERE inhrelid = v_existing AND inhparent = v_parent
        ) THEN
            RAISE EXCEPTION 'spinneret_ensure_partitions: "%" exists but is not a partition of %', v_name, v_parent
                USING ERRCODE = 'duplicate_table';
        END IF;

        v_start := v_end;
    END LOOP;

    RETURN v_created;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION spinneret_drop_partitions_before(
    p_table       text,
    p_granularity text,
    p_before      timestamptz
) RETURNS integer
LANGUAGE plpgsql
SET "TimeZone" = 'UTC'
SET "DateStyle" = 'ISO, YMD'
AS $$
DECLARE
    v_parent  regclass;
    v_relname text;
    v_digits  integer;
    v_pattern text;
    v_upper   timestamptz;
    v_locked  boolean := false;
    v_dropped integer := 0;
    r         record;
BEGIN
    IF p_granularity = 'day' THEN
        v_digits := 8;
    ELSIF p_granularity = 'month' THEN
        v_digits := 6;
    ELSE
        RAISE EXCEPTION 'spinneret_drop_partitions_before: unsupported granularity "%" (want day or month)', p_granularity
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    IF p_before IS NULL THEN
        RAISE EXCEPTION 'spinneret_drop_partitions_before: cutoff must not be null'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT c.oid::regclass, c.relname
      INTO v_parent, v_relname
      FROM pg_class c
     WHERE c.oid = to_regclass(p_table)
       AND c.relkind = 'p';

    IF v_parent IS NULL THEN
        RAISE EXCEPTION 'spinneret_drop_partitions_before: "%" is not a partitioned table', p_table
            USING ERRCODE = 'undefined_table';
    END IF;

    v_pattern := '^_p[0-9]{' || v_digits || '}$';

    -- Only partitions following the managed naming scheme are considered; the
    -- upper bound is read from the partition definition itself so that a
    -- partition is dropped only when every row it can hold is older than the cutoff.
    --
    -- The first scan runs without locks so that the common "nothing expired"
    -- case never blocks readers or writers. As soon as an expired partition is
    -- found the parent is locked (dropping a partition needs this lock anyway)
    -- and the catalog is scanned again: a concurrent call that dropped the same
    -- partitions has committed by then, so they are never dropped twice.
    <<scan>>
    LOOP
        FOR r IN
            SELECT n.nspname,
                   c.relname,
                   pg_get_expr(c.relpartbound, c.oid) AS bound
              FROM pg_inherits i
              JOIN pg_class c ON c.oid = i.inhrelid
              JOIN pg_namespace n ON n.oid = c.relnamespace
             WHERE i.inhparent = v_parent
             ORDER BY c.relname
        LOOP
            CONTINUE WHEN left(r.relname, length(v_relname)) <> v_relname;
            CONTINUE WHEN substr(r.relname, length(v_relname) + 1) !~ v_pattern;

            v_upper := substring(r.bound FROM 'TO \(''([^'']+)''\)')::timestamptz;
            CONTINUE WHEN v_upper IS NULL OR v_upper > p_before;

            IF NOT v_locked THEN
                EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE', v_parent);
                v_locked := true;
                CONTINUE scan;
            END IF;

            EXECUTE format('DROP TABLE %I.%I', r.nspname, r.relname);
            v_dropped := v_dropped + 1;
        END LOOP;
        EXIT scan;
    END LOOP;

    RETURN v_dropped;
END;
$$;
-- +goose StatementEnd

-- Lifecycle state transitions, cooldowns and manual operations (monthly).
CREATE TABLE state_events (
    id                text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    tenant_id         text        NOT NULL,
    namespace_id      text        NOT NULL,
    site_id           text        NOT NULL DEFAULT '',
    subject_kind      text        NOT NULL,
    subject_id        text        NOT NULL,
    endpoint_group_id text        NOT NULL DEFAULT '',
    from_state        text        NOT NULL DEFAULT '',
    to_state          text        NOT NULL DEFAULT '',
    action            text        NOT NULL DEFAULT '',
    scope             text        NOT NULL DEFAULT '',
    until             timestamptz,
    permanent         boolean     NOT NULL DEFAULT false,
    outcome           text        NOT NULL DEFAULT '',
    policy_id         text        NOT NULL DEFAULT '',
    policy_version    integer     NOT NULL DEFAULT 0,
    rule              text        NOT NULL DEFAULT '',
    report_id         text        NOT NULL DEFAULT '',
    lease_id          text        NOT NULL DEFAULT '',
    actor             text        NOT NULL DEFAULT '',
    reason            text        NOT NULL DEFAULT '',
    shadow            boolean     NOT NULL DEFAULT false,
    details           jsonb       NOT NULL DEFAULT '{}',
    PRIMARY KEY (id, created_at),
    CONSTRAINT state_events_subject_kind_check CHECK (
        subject_kind IN ('identity', 'account', 'proxy', 'endpoint_group', 'site')
    )
) PARTITION BY RANGE (created_at);

CREATE INDEX state_events_subject_id_created_at_idx ON state_events (subject_id, created_at DESC);
CREATE INDEX state_events_namespace_id_created_at_idx ON state_events (namespace_id, created_at DESC);
CREATE INDEX state_events_rule_created_at_idx ON state_events (rule, created_at);

-- Non-success report details (daily).
CREATE TABLE risk_events (
    id                text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    tenant_id         text        NOT NULL,
    namespace_id      text        NOT NULL,
    site_id           text        NOT NULL,
    endpoint_group_id text        NOT NULL DEFAULT '',
    identity_id       text        NOT NULL DEFAULT '',
    proxy_id          text        NOT NULL DEFAULT '',
    lease_id          text        NOT NULL DEFAULT '',
    report_id         text        NOT NULL DEFAULT '',
    node              text        NOT NULL DEFAULT '',
    token_id          text        NOT NULL DEFAULT '',
    uri               text        NOT NULL DEFAULT '',
    method            text        NOT NULL DEFAULT '',
    http_status       integer     NOT NULL DEFAULT 0,
    business_code     text        NOT NULL DEFAULT '',
    error_kind        text        NOT NULL DEFAULT '',
    markers           text[]      NOT NULL DEFAULT '{}',
    outcome           text        NOT NULL,
    blame             text        NOT NULL DEFAULT '',
    rule              text        NOT NULL DEFAULT '',
    latency_ms        integer     NOT NULL DEFAULT 0,
    response_bytes    bigint      NOT NULL DEFAULT 0,
    started_at        timestamptz,
    finished_at       timestamptz,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX risk_events_namespace_id_site_id_created_at_idx ON risk_events (namespace_id, site_id, created_at DESC);

-- Per-minute outcome aggregates (daily).
CREATE TABLE outcome_stats_minutely (
    bucket             timestamptz NOT NULL,
    namespace_id       text        NOT NULL,
    site_id            text        NOT NULL,
    endpoint_group_id  text        NOT NULL DEFAULT '',
    proxy_id           text        NOT NULL DEFAULT '',
    outcome            text        NOT NULL,
    count              bigint      NOT NULL DEFAULT 0,
    latency_ms_sum     bigint      NOT NULL DEFAULT 0,
    response_bytes_sum bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome)
) PARTITION BY RANGE (bucket);

-- Per-hour identity outcome aggregates (monthly).
CREATE TABLE identity_stats_hourly (
    bucket            timestamptz NOT NULL,
    site_id           text        NOT NULL,
    identity_id       text        NOT NULL,
    endpoint_group_id text        NOT NULL DEFAULT '',
    outcome           text        NOT NULL,
    count             bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, identity_id, endpoint_group_id, outcome)
) PARTITION BY RANGE (bucket);

CREATE INDEX identity_stats_hourly_identity_id_bucket_idx ON identity_stats_hourly (identity_id, bucket DESC);

-- Per-minute acquire results (daily).
CREATE TABLE acquire_stats_minutely (
    bucket            timestamptz NOT NULL,
    namespace_id      text        NOT NULL,
    site_id           text        NOT NULL,
    endpoint_group_id text        NOT NULL DEFAULT '',
    result            text        NOT NULL,
    count             bigint      NOT NULL DEFAULT 0,
    duration_us_sum   bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, namespace_id, site_id, endpoint_group_id, result),
    CONSTRAINT acquire_stats_minutely_result_check CHECK (
        result IN ('ok', 'exhausted', 'circuit_open', 'site_paused', 'no_proxy', 'error')
    )
) PARTITION BY RANGE (bucket);

-- Per-minute node activity (daily).
CREATE TABLE node_stats_minutely (
    bucket       timestamptz NOT NULL,
    namespace_id text        NOT NULL,
    node         text        NOT NULL,
    acquires     bigint      NOT NULL DEFAULT 0,
    reports      bigint      NOT NULL DEFAULT 0,
    abandoned    bigint      NOT NULL DEFAULT 0,
    rejected     bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, namespace_id, node)
) PARTITION BY RANGE (bucket);

-- Per-minute credential payload deliveries (daily).
CREATE TABLE payload_access_minutely (
    bucket           timestamptz NOT NULL,
    namespace_id     text        NOT NULL,
    token_id         text        NOT NULL,
    identity_type_id text        NOT NULL,
    count            bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, namespace_id, token_id, identity_type_id)
) PARTITION BY RANGE (bucket);

-- Audit trail (monthly).
CREATE TABLE audit_logs (
    id            text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    tenant_id     text        NOT NULL DEFAULT '',
    namespace_id  text        NOT NULL DEFAULT '',
    actor_kind    text        NOT NULL,
    actor_id      text        NOT NULL DEFAULT '',
    actor_name    text        NOT NULL DEFAULT '',
    action        text        NOT NULL,
    resource_kind text        NOT NULL DEFAULT '',
    resource_id   text        NOT NULL DEFAULT '',
    resource_name text        NOT NULL DEFAULT '',
    result        text        NOT NULL DEFAULT 'ok',
    ip            text        NOT NULL DEFAULT '',
    user_agent    text        NOT NULL DEFAULT '',
    details       jsonb       NOT NULL DEFAULT '{}',
    PRIMARY KEY (id, created_at),
    CONSTRAINT audit_logs_actor_kind_check CHECK (actor_kind IN ('user', 'token', 'system')),
    CONSTRAINT audit_logs_result_check CHECK (result IN ('ok', 'denied', 'error'))
) PARTITION BY RANGE (created_at);

CREATE INDEX audit_logs_tenant_id_created_at_idx ON audit_logs (tenant_id, created_at DESC);

-- Initial partitions: [now - 2 periods, now + 7 days] for daily tables and
-- [now - 2 periods, now + 3 months] for monthly tables.
SELECT spinneret_ensure_partitions('state_events', 'month', now() - interval '2 months', now() + interval '3 months');
SELECT spinneret_ensure_partitions('risk_events', 'day', now() - interval '2 days', now() + interval '7 days');
SELECT spinneret_ensure_partitions('outcome_stats_minutely', 'day', now() - interval '2 days', now() + interval '7 days');
SELECT spinneret_ensure_partitions('identity_stats_hourly', 'month', now() - interval '2 months', now() + interval '3 months');
SELECT spinneret_ensure_partitions('acquire_stats_minutely', 'day', now() - interval '2 days', now() + interval '7 days');
SELECT spinneret_ensure_partitions('node_stats_minutely', 'day', now() - interval '2 days', now() + interval '7 days');
SELECT spinneret_ensure_partitions('payload_access_minutely', 'day', now() - interval '2 days', now() + interval '7 days');
SELECT spinneret_ensure_partitions('audit_logs', 'month', now() - interval '2 months', now() + interval '3 months');

-- +goose Down

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS payload_access_minutely;
DROP TABLE IF EXISTS node_stats_minutely;
DROP TABLE IF EXISTS acquire_stats_minutely;
DROP TABLE IF EXISTS identity_stats_hourly;
DROP TABLE IF EXISTS outcome_stats_minutely;
DROP TABLE IF EXISTS risk_events;
DROP TABLE IF EXISTS state_events;
DROP FUNCTION IF EXISTS spinneret_drop_partitions_before(text, text, timestamptz);
DROP FUNCTION IF EXISTS spinneret_ensure_partitions(text, text, timestamptz, timestamptz);
