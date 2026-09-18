package clickhouse

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Table names.
const (
	ReportEventsTable = "report_events"
	LeaseEventsTable  = "lease_events"
)

// MaxTTLDays is the largest retention accepted by Migrate. The TTL expression
// is evaluated as a 32-bit DateTime (upper bound 2106-02-07); a longer
// retention would overflow and wrap to a past date, making ClickHouse delete
// rows as soon as they are inserted.
const MaxTTLDays = 3650

const (
	lowCardinality = "LowCardinality(String)"
	timestamp      = "DateTime64(3, 'UTC')"
)

// column is one column of a managed table.
type column struct {
	name string
	typ  string
}

// skipIndex is a data skipping index of a managed table.
type skipIndex struct {
	name string
	expr string
}

// tableSpec describes a managed MergeTree table partitioned by day on event_time.
type tableSpec struct {
	name    string
	columns []column
	orderBy string
	indexes []skipIndex
}

// reportEventsSpec returns the schema of report_events (one row per report).
func reportEventsSpec() tableSpec {
	return tableSpec{
		name: ReportEventsTable,
		columns: []column{
			{"event_time", timestamp + " CODEC(DoubleDelta, ZSTD(1))"},
			{"received_at", timestamp},
			{"started_at", timestamp},
			{"tenant_id", lowCardinality},
			{"namespace_id", lowCardinality},
			{"site_id", lowCardinality},
			{"site", lowCardinality},
			{"endpoint_group", lowCardinality},
			{"client", lowCardinality},
			{"identity_id", "String"},
			{"identity_type", lowCardinality},
			{"proxy_id", "String"},
			{"lease_id", "String"},
			{"report_id", "String"},
			{"node", lowCardinality},
			{"token_id", lowCardinality},
			{"uri", "String"},
			{"method", lowCardinality},
			{"http_status", "UInt16"},
			{"business_code", lowCardinality},
			{"error_kind", lowCardinality},
			{"markers", "Array(LowCardinality(String))"},
			{"outcome", lowCardinality},
			{"outcome_hint", lowCardinality},
			{"blame", lowCardinality},
			{"rule", lowCardinality},
			{"latency_ms", "UInt32"},
			{"response_bytes", "UInt64"},
			{"suppressed", "UInt8"},
			{"late", "UInt8"},
			{"probe", "UInt8"},
		},
		orderBy: "(namespace_id, site, endpoint_group, event_time)",
		indexes: []skipIndex{
			{"idx_identity_id", "identity_id TYPE bloom_filter(0.01) GRANULARITY 4"},
			{"idx_proxy_id", "proxy_id TYPE bloom_filter(0.01) GRANULARITY 4"},
			{"idx_lease_id", "lease_id TYPE bloom_filter(0.01) GRANULARITY 4"},
			{"idx_report_id", "report_id TYPE bloom_filter(0.01) GRANULARITY 4"},
		},
	}
}

// leaseEventsSpec returns the schema of lease_events (one row per lease lifecycle event).
func leaseEventsSpec() tableSpec {
	return tableSpec{
		name: LeaseEventsTable,
		columns: []column{
			{"event_time", timestamp + " CODEC(DoubleDelta, ZSTD(1))"},
			{"tenant_id", lowCardinality},
			{"namespace_id", lowCardinality},
			{"site_id", lowCardinality},
			{"site", lowCardinality},
			{"endpoint_group", lowCardinality},
			{"client", lowCardinality},
			{"identity_id", "String"},
			{"proxy_id", "String"},
			{"lease_id", "String"},
			{"node", lowCardinality},
			{"token_id", lowCardinality},
			{"event", lowCardinality},
			{"result", lowCardinality},
			{"duration_us", "UInt32"},
			{"probe", "UInt8"},
			{"sticky", "UInt8"},
		},
		orderBy: "(namespace_id, site, endpoint_group, event_time)",
		indexes: []skipIndex{
			{"idx_identity_id", "identity_id TYPE bloom_filter(0.01) GRANULARITY 4"},
			{"idx_lease_id", "lease_id TYPE bloom_filter(0.01) GRANULARITY 4"},
		},
	}
}

// columnNames returns the comma-separated column list used by INSERT statements.
func (s tableSpec) columnNames() string {
	names := make([]string, len(s.columns))
	for i, c := range s.columns {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

// insertSQL returns the INSERT statement prepared for batches.
func (s tableSpec) insertSQL() string {
	return "INSERT INTO " + s.name + " (" + s.columnNames() + ")"
}

// ttlExpr returns the TTL expression for ttlDays.
func ttlExpr(ttlDays int) string {
	return "toDateTime(event_time) + INTERVAL " + strconv.Itoa(ttlDays) + " DAY"
}

// createSQL returns the CREATE TABLE IF NOT EXISTS statement.
func (s tableSpec) createSQL(ttlDays int) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS ")
	b.WriteString(s.name)
	b.WriteString(" (\n")
	for i, c := range s.columns {
		b.WriteString("    ")
		b.WriteString(c.name)
		b.WriteByte(' ')
		b.WriteString(c.typ)
		if i < len(s.columns)-1 || len(s.indexes) > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	for i, idx := range s.indexes {
		b.WriteString("    INDEX ")
		b.WriteString(idx.name)
		b.WriteByte(' ')
		b.WriteString(idx.expr)
		if i < len(s.indexes)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(") ENGINE = MergeTree\nPARTITION BY toYYYYMMDD(event_time)\nORDER BY ")
	b.WriteString(s.orderBy)
	b.WriteString("\nTTL ")
	b.WriteString(ttlExpr(ttlDays))
	b.WriteString("\nSETTINGS index_granularity = 8192, ttl_only_drop_parts = 1")
	return b.String()
}

// ttlDaysPattern extracts the day count from the normalized TTL in system.tables.engine_full.
var ttlDaysPattern = regexp.MustCompile(`TTL\s+toDateTime\(event_time\)\s*\+\s*toIntervalDay\((\d+)\)`)

// Migrate creates report_events and lease_events in the connection's current
// database when they are missing. For existing tables it adds missing columns
// and skipping indexes and, when the retention differs, runs
// ALTER TABLE ... MODIFY TTL. It is idempotent and safe to run concurrently
// from several instances. Column types of existing columns are never changed.
func Migrate(ctx context.Context, conn chdriver.Conn, ttlDays int) error {
	if conn == nil {
		return fmt.Errorf("clickhouse: migrate: nil connection")
	}
	if ttlDays < 1 || ttlDays > MaxTTLDays {
		return fmt.Errorf("clickhouse: migrate: ttl days must be between 1 and %d, got %d", MaxTTLDays, ttlDays)
	}
	for _, spec := range []tableSpec{reportEventsSpec(), leaseEventsSpec()} {
		if err := migrateTable(ctx, conn, spec, ttlDays); err != nil {
			return fmt.Errorf("clickhouse: migrate %s: %w", spec.name, err)
		}
	}
	return nil
}

func migrateTable(ctx context.Context, conn chdriver.Conn, spec tableSpec, ttlDays int) error {
	engineFull, exists, err := tableEngine(ctx, conn, spec.name)
	if err != nil {
		return err
	}
	if !exists {
		if err := conn.Exec(ctx, spec.createSQL(ttlDays)); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
		return nil
	}
	if err := ensureColumns(ctx, conn, spec); err != nil {
		return err
	}
	if err := ensureIndexes(ctx, conn, spec); err != nil {
		return err
	}
	if current, ok := parseTTLDays(engineFull); !ok || current != ttlDays {
		if err := conn.Exec(ctx, "ALTER TABLE "+spec.name+" MODIFY TTL "+ttlExpr(ttlDays)); err != nil {
			return fmt.Errorf("modify ttl: %w", err)
		}
	}
	return nil
}

// tableEngine returns system.tables.engine_full of table in the current database.
func tableEngine(ctx context.Context, conn chdriver.Conn, table string) (string, bool, error) {
	rows, err := conn.Query(ctx,
		"SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?", table)
	if err != nil {
		return "", false, fmt.Errorf("inspect table: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", false, fmt.Errorf("inspect table: %w", err)
		}
		return "", false, nil
	}
	var engine string
	if err := rows.Scan(&engine); err != nil {
		return "", false, fmt.Errorf("scan table engine: %w", err)
	}
	return engine, true, nil
}

// parseTTLDays extracts the TTL day count from engine_full.
func parseTTLDays(engineFull string) (int, bool) {
	m := ttlDaysPattern.FindStringSubmatch(engineFull)
	if m == nil {
		return 0, false
	}
	days, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return days, true
}

// existingNames returns the values of the single-column query result as a set.
func existingNames(ctx context.Context, conn chdriver.Conn, query, table string) (map[string]struct{}, error) {
	rows, err := conn.Query(ctx, query, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = struct{}{}
	}
	return names, rows.Err()
}

func ensureColumns(ctx context.Context, conn chdriver.Conn, spec tableSpec) error {
	have, err := existingNames(ctx, conn,
		"SELECT name FROM system.columns WHERE database = currentDatabase() AND table = ?", spec.name)
	if err != nil {
		return fmt.Errorf("inspect columns: %w", err)
	}
	for i, c := range spec.columns {
		if _, ok := have[c.name]; ok {
			continue
		}
		stmt := "ALTER TABLE " + spec.name + " ADD COLUMN IF NOT EXISTS " + c.name + " " + c.typ
		if i > 0 {
			stmt += " AFTER " + spec.columns[i-1].name
		} else {
			stmt += " FIRST"
		}
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	return nil
}

func ensureIndexes(ctx context.Context, conn chdriver.Conn, spec tableSpec) error {
	have, err := existingNames(ctx, conn,
		"SELECT name FROM system.data_skipping_indices WHERE database = currentDatabase() AND table = ?", spec.name)
	if err != nil {
		return fmt.Errorf("inspect indexes: %w", err)
	}
	for _, idx := range spec.indexes {
		if _, ok := have[idx.name]; ok {
			continue
		}
		if err := conn.Exec(ctx, "ALTER TABLE "+spec.name+" ADD INDEX IF NOT EXISTS "+idx.name+" "+idx.expr); err != nil {
			return fmt.Errorf("add index %s: %w", idx.name, err)
		}
	}
	return nil
}
