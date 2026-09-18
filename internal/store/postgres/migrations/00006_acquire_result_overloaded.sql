-- Acquire admission control sheds requests before they reach Redis and records
-- them as result 'overloaded'. Without this widening the first overload event
-- would also break statistics ingestion: every flush batch containing a shed
-- row would violate acquire_stats_minutely_result_check (00002_partitioned.sql).
--
-- 'overloaded' is deliberately distinct from 'exhausted': exhausted means the
-- identity pool is empty (add identities, widen the rotation policy) while
-- overloaded means this instance is at its acquire concurrency limit (add
-- capacity or offer less load). The performance guide alerts on the two
-- separately.
--
-- NO TRANSACTION keeps the ACCESS EXCLUSIVE lock of the DROP/ADD short and
-- lets VALIDATE CONSTRAINT run under SHARE UPDATE EXCLUSIVE afterwards, so a
-- large installation is not blocked for the length of a full partition scan.

-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE acquire_stats_minutely DROP CONSTRAINT IF EXISTS acquire_stats_minutely_result_check;

ALTER TABLE acquire_stats_minutely ADD CONSTRAINT acquire_stats_minutely_result_check CHECK (
    result IN ('ok', 'exhausted', 'circuit_open', 'site_paused', 'no_proxy', 'error', 'overloaded')
) NOT VALID;

ALTER TABLE acquire_stats_minutely VALIDATE CONSTRAINT acquire_stats_minutely_result_check;

-- +goose Down

DELETE FROM acquire_stats_minutely WHERE result = 'overloaded';

ALTER TABLE acquire_stats_minutely DROP CONSTRAINT IF EXISTS acquire_stats_minutely_result_check;

ALTER TABLE acquire_stats_minutely ADD CONSTRAINT acquire_stats_minutely_result_check CHECK (
    result IN ('ok', 'exhausted', 'circuit_open', 'site_paused', 'no_proxy', 'error')
);
