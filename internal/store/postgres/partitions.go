package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/appconfig"
)

// Granularity is the period covered by one partition of a range-partitioned table.
type Granularity string

// Supported partition granularities.
const (
	GranularityDay   Granularity = "day"
	GranularityMonth Granularity = "month"
)

// PartitionedTable describes a range-partitioned table maintained by the
// partition manager.
type PartitionedTable struct {
	Name        string
	Granularity Granularity
}

// Partitioned table names.
const (
	TableStateEvents           = "state_events"
	TableRiskEvents            = "risk_events"
	TableOutcomeStatsMinutely  = "outcome_stats_minutely"
	TableIdentityStatsHourly   = "identity_stats_hourly"
	TableAcquireStatsMinutely  = "acquire_stats_minutely"
	TableNodeStatsMinutely     = "node_stats_minutely"
	TablePayloadAccessMinutely = "payload_access_minutely"
	TableAuditLogs             = "audit_logs"
)

// partitionLockTimeout bounds how long partition DDL waits for the lock on the
// parent table. Creating or dropping a partition needs an exclusive lock on
// the parent; without a timeout a long-running reader would make every writer
// of the table queue behind the DDL.
const partitionLockTimeout = 10 * time.Second

// PartitionedTables returns every partitioned table with its granularity, in
// migration order. The returned slice is a fresh copy.
func PartitionedTables() []PartitionedTable {
	return []PartitionedTable{
		{Name: TableStateEvents, Granularity: GranularityMonth},
		{Name: TableRiskEvents, Granularity: GranularityDay},
		{Name: TableOutcomeStatsMinutely, Granularity: GranularityDay},
		{Name: TableIdentityStatsHourly, Granularity: GranularityMonth},
		{Name: TableAcquireStatsMinutely, Granularity: GranularityDay},
		{Name: TableNodeStatsMinutely, Granularity: GranularityDay},
		{Name: TablePayloadAccessMinutely, Granularity: GranularityDay},
		{Name: TableAuditLogs, Granularity: GranularityMonth},
	}
}

// EnsurePartitionWindow returns the range EnsurePartitions keeps covered for a
// granularity relative to now: [now-1 day, now+7 days] for daily tables and
// [start of previous month, start of the month three months ahead] for
// monthly tables. Month arithmetic starts from the first day of the month so
// that short months are never skipped.
func EnsurePartitionWindow(g Granularity, now time.Time) (from, to time.Time, err error) {
	now = now.UTC()
	switch g {
	case GranularityDay:
		return now.AddDate(0, 0, -1), now.AddDate(0, 0, 7), nil
	case GranularityMonth:
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return monthStart.AddDate(0, -1, 0), monthStart.AddDate(0, 3, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported partition granularity %q", g)
	}
}

// EnsurePartitions creates the missing partitions of every partitioned table
// for the window returned by EnsurePartitionWindow. It is idempotent and safe
// to run concurrently. Every table is attempted; failures are joined.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	if pool == nil {
		return errors.New("ensure partitions: pool is nil")
	}
	var errs []error
	for _, t := range PartitionedTables() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, fmt.Errorf("ensure partitions: %w", err))...)
		}
		from, to, err := EnsurePartitionWindow(t.Granularity, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("ensure partitions of %s: %w", t.Name, err))
			continue
		}
		err = execPartitionDDL(ctx, pool,
			"SELECT spinneret_ensure_partitions($1, $2, $3, $4)", t.Name, string(t.Granularity), from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("ensure partitions of %s: %w", t.Name, err))
		}
	}
	return errors.Join(errs...)
}

// RetentionFor returns the retention period that applies to a partitioned
// table, or 0 when the table is unknown.
func RetentionFor(table string, r appconfig.Retention) time.Duration {
	switch table {
	case TableRiskEvents:
		return r.RiskEvents
	case TableOutcomeStatsMinutely, TableAcquireStatsMinutely, TableNodeStatsMinutely, TablePayloadAccessMinutely:
		return r.MinuteStats
	case TableIdentityStatsHourly:
		return r.HourStats
	case TableStateEvents:
		return r.StateEvents
	case TableAuditLogs:
		return r.Audit
	default:
		return 0
	}
}

// DropExpiredPartitions drops, for every partitioned table, the partitions
// whose upper bound is not after now minus the table's retention (see
// RetentionFor), so only partitions holding exclusively expired rows are
// removed. Tables with a non-positive retention are kept forever. It is
// idempotent and safe to run concurrently: the parent table is only locked
// when a partition has actually expired. Every table is attempted; failures
// are joined.
func DropExpiredPartitions(ctx context.Context, pool *pgxpool.Pool, r appconfig.Retention, now time.Time) error {
	if pool == nil {
		return errors.New("drop expired partitions: pool is nil")
	}
	var errs []error
	for _, t := range PartitionedTables() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, fmt.Errorf("drop expired partitions: %w", err))...)
		}
		retention := RetentionFor(t.Name, r)
		if retention <= 0 {
			continue
		}
		before := now.UTC().Add(-retention)
		err := execPartitionDDL(ctx, pool,
			"SELECT spinneret_drop_partitions_before($1, $2, $3)", t.Name, string(t.Granularity), before)
		if err != nil {
			errs = append(errs, fmt.Errorf("drop expired partitions of %s: %w", t.Name, err))
		}
	}
	return errors.Join(errs...)
}

// execPartitionDDL runs a partition management function in its own
// transaction with a bounded lock_timeout.
func execPartitionDDL(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) error {
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		timeout := fmt.Sprintf("%dms", partitionLockTimeout.Milliseconds())
		if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", timeout); err != nil {
			return fmt.Errorf("set lock_timeout: %w", err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
		return nil
	})
}
