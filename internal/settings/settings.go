// Package settings holds the deployment-wide values an operator can change while
// the server runs, rather than by editing the environment and restarting.
//
// Only values that are safe to change on a running deployment belong here. The
// test for that is not "would someone like to change it" but "what happens if it
// changes between two reads". Everything in this package is read by the hourly
// partition job or by a schema migration, never by the acquire or report hot
// paths, so a change takes effect on the next pass and can never be observed
// half-applied by a request. Values that are encoded into data (the report shard
// count lives in every lease id), needed before the database can be reached
// (its own URL, the key-encryption keys) or that must not vary at all are
// deliberately absent and stay in the environment.
//
// # Where a value comes from
//
// Three sources, in order:
//
//  1. the environment, when the variable is explicitly set — it wins and the
//     setting becomes read-only, so that a deployment managed from a file keeps
//     the guarantee that the file is what runs;
//  2. the database, when a row has been written by the console;
//  3. the built-in default.
//
// Nothing in this project's shipped .env writes a retention variable, so for an
// existing deployment every one of these starts at the default and the database
// can take them over without changing any current behaviour.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// storeKey is the system_settings row this package owns. One row holding one
// JSON object, so that changing several values is a single atomic write and
// reading them is a single query.
const storeKey = "retention"

// Bounds shared with the rest of the system.
const (
	// MinRetention is the floor for every PostgreSQL retention. A partition is
	// only dropped once its whole range has expired, and partitions are daily,
	// so anything below a day cannot drop anything anyway.
	MinRetention = 24 * time.Hour
	// MaxRetention is a sanity ceiling, not a technical limit: past it the
	// partition job would be asked to keep partitions for longer than the
	// project has existed, which is more likely a typo than an intention.
	MaxRetention = 10 * 365 * 24 * time.Hour
	// MinTTLDays and MaxTTLDays bound the ClickHouse TTL, matching what the
	// ClickHouse migration itself accepts.
	MinTTLDays = 1
	MaxTTLDays = 3650
)

// Keys of the settings this package manages. They are stable identifiers: they
// appear in the API, so renaming one is a breaking change.
const (
	KeyRiskEvents  = "retention.risk_events"
	KeyMinuteStats = "retention.minute_stats"
	KeyHourStats   = "retention.hour_stats"
	KeyStateEvents = "retention.state_events"
	KeyAudit       = "retention.audit"
	KeyAlertEvents = "retention.alert_events"
	KeyClickHouse  = "retention.clickhouse_ttl_days"
)

// Origin is where a setting's effective value came from.
type Origin string

// The three origins, in precedence order.
const (
	OriginDefault     Origin = "default"
	OriginDatabase    Origin = "database"
	OriginEnvironment Origin = "environment"
)

// Unit tells a caller how to render and validate a value.
type Unit string

// The units in use.
const (
	UnitDuration Unit = "duration"
	UnitDays     Unit = "days"
)

// Setting is one value with everything a caller needs to show and check it.
type Setting struct {
	Key     string
	Value   string
	Default string
	Origin  Origin
	// EnvVar is the variable that pins this setting, set whatever the origin is
	// so that an operator can be told which variable to remove to unpin it.
	EnvVar string
	Unit   Unit
	Min    string
	Max    string
}

// Editable reports whether Update may change this setting.
func (s Setting) Editable() bool { return s.Origin != OriginEnvironment }

// Defaults are the built-in values, used when neither the environment nor the
// database has an opinion. They match the documented defaults.
type Defaults struct {
	RiskEvents        time.Duration
	MinuteStats       time.Duration
	HourStats         time.Duration
	StateEvents       time.Duration
	Audit             time.Duration
	AlertEvents       time.Duration
	ClickHouseTTLDays int
}

// Resolved is the effective configuration, after the environment, the database
// and the defaults have been merged.
type Resolved struct {
	RiskEvents        time.Duration
	MinuteStats       time.Duration
	HourStats         time.Duration
	StateEvents       time.Duration
	Audit             time.Duration
	AlertEvents       time.Duration
	ClickHouseTTLDays int
}

// Config configures a Store.
type Config struct {
	// Defaults are the values the environment loader resolved, which are the
	// built-in defaults for every variable the environment did not set.
	Defaults Defaults
	// FromEnv names the settings whose environment variable was explicitly set.
	// Those are pinned: their value in Defaults is what the environment said,
	// and the database cannot override them.
	FromEnv map[string]bool
}

// Querier is the part of the generated database layer this package uses.
type Querier interface {
	GetSystemSetting(ctx context.Context, key string) (db.SystemSetting, error)
	UpsertSystemSetting(ctx context.Context, arg db.UpsertSystemSettingParams) (db.SystemSetting, error)
}

// Store reads and writes the deployment settings.
//
// It deliberately holds no cache. Every value here is read by the hourly
// partition job or when the console asks, so one query per hour is not worth a
// cache — and a cache would add the question of how long a change takes to reach
// the instance that happens to be the leader, which is exactly the confusion an
// operator changing a retention does not need.
type Store struct {
	q   Querier
	cfg Config
}

// New creates a Store. A nil Querier makes every read return the environment and
// default values and every write fail, which is what a deployment without a
// database would see — it cannot happen in practice and is not special-cased
// beyond not panicking.
func New(q Querier, cfg Config) *Store {
	if cfg.FromEnv == nil {
		cfg.FromEnv = map[string]bool{}
	}
	return &Store{q: q, cfg: cfg}
}

// stored is the JSON object in the system_settings row. Every field is a string
// and omitted when unset, so a value the database has no opinion about falls
// through to the environment or the default, and so the row stays readable to a
// human looking at it with psql.
type stored struct {
	RiskEvents  string `json:"risk_events,omitempty"`
	MinuteStats string `json:"minute_stats,omitempty"`
	HourStats   string `json:"hour_stats,omitempty"`
	StateEvents string `json:"state_events,omitempty"`
	Audit       string `json:"audit,omitempty"`
	AlertEvents string `json:"alert_events,omitempty"`
	ClickHouse  string `json:"clickhouse_ttl_days,omitempty"`
}

func (s *stored) field(key string) *string {
	switch key {
	case KeyRiskEvents:
		return &s.RiskEvents
	case KeyMinuteStats:
		return &s.MinuteStats
	case KeyHourStats:
		return &s.HourStats
	case KeyStateEvents:
		return &s.StateEvents
	case KeyAudit:
		return &s.Audit
	case KeyAlertEvents:
		return &s.AlertEvents
	case KeyClickHouse:
		return &s.ClickHouse
	}
	return nil
}

// spec describes one setting: where its default comes from and how it is read.
type spec struct {
	key    string
	envVar string
	unit   Unit
}

// specs is the whole set, in the order the console shows them: the two an
// operator shortens first when PostgreSQL disk is the constraint, then the long
// ones, then the stores that are not PostgreSQL.
var specs = []spec{
	{KeyRiskEvents, "SPINNERET_RETENTION_RISK_EVENTS", UnitDuration},
	{KeyMinuteStats, "SPINNERET_RETENTION_MINUTE_STATS", UnitDuration},
	{KeyHourStats, "SPINNERET_RETENTION_HOUR_STATS", UnitDuration},
	{KeyStateEvents, "SPINNERET_RETENTION_STATE_EVENTS", UnitDuration},
	{KeyAudit, "SPINNERET_RETENTION_AUDIT", UnitDuration},
	{KeyAlertEvents, "", UnitDuration},
	{KeyClickHouse, "SPINNERET_CLICKHOUSE_TTL_DAYS", UnitDays},
}

func (c Config) defaultOf(key string) string {
	switch key {
	case KeyRiskEvents:
		return durationx.Duration(c.Defaults.RiskEvents).String()
	case KeyMinuteStats:
		return durationx.Duration(c.Defaults.MinuteStats).String()
	case KeyHourStats:
		return durationx.Duration(c.Defaults.HourStats).String()
	case KeyStateEvents:
		return durationx.Duration(c.Defaults.StateEvents).String()
	case KeyAudit:
		return durationx.Duration(c.Defaults.Audit).String()
	case KeyAlertEvents:
		return durationx.Duration(c.Defaults.AlertEvents).String()
	case KeyClickHouse:
		return strconv.Itoa(c.Defaults.ClickHouseTTLDays)
	}
	return ""
}

// load reads the row. A missing row is an empty object, not an error: a
// deployment that has never opened the settings page has no row.
func (s *Store) load(ctx context.Context) (stored, error) {
	var out stored
	if s.q == nil {
		return out, nil
	}
	row, err := s.q.GetSystemSetting(ctx, storeKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read deployment settings: %w", err)
	}
	if len(row.Value) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(row.Value, &out); err != nil {
		// A row that cannot be parsed must not take the deployment's retention
		// with it: report it and let every setting fall back.
		return stored{}, fmt.Errorf("decode deployment settings: %w", err)
	}
	return out, nil
}

// List returns every setting with its effective value and where it came from.
func (s *Store) List(ctx context.Context) ([]Setting, error) {
	saved, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return s.merge(saved), nil
}

func (s *Store) merge(saved stored) []Setting {
	out := make([]Setting, 0, len(specs))
	for _, sp := range specs {
		def := s.cfg.defaultOf(sp.key)
		set := Setting{
			Key:     sp.key,
			Value:   def,
			Default: def,
			Origin:  OriginDefault,
			EnvVar:  sp.envVar,
			Unit:    sp.unit,
		}
		switch sp.unit {
		case UnitDuration:
			set.Min = durationx.Duration(MinRetention).String()
			set.Max = durationx.Duration(MaxRetention).String()
		case UnitDays:
			set.Min = strconv.Itoa(MinTTLDays)
			set.Max = strconv.Itoa(MaxTTLDays)
		}
		switch {
		case s.cfg.FromEnv[sp.key]:
			// Defaults already carries what the environment resolved.
			set.Origin = OriginEnvironment
		default:
			if f := saved.field(sp.key); f != nil && *f != "" {
				set.Value = *f
				set.Origin = OriginDatabase
			}
		}
		out = append(out, set)
	}
	return out
}

// Resolve returns the effective configuration. A database that cannot be read
// is an error rather than a silent fall back to defaults: shortening a retention
// and having it silently not apply is the kind of thing an operator finds out
// about from a full disk.
func (s *Store) Resolve(ctx context.Context) (Resolved, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{}
	for _, set := range list {
		switch set.Key {
		case KeyClickHouse:
			n, err := strconv.Atoi(set.Value)
			if err != nil {
				return Resolved{}, fmt.Errorf("%s: %q is not a number", set.Key, set.Value)
			}
			out.ClickHouseTTLDays = n
			continue
		}
		d, err := durationx.Parse(set.Value)
		if err != nil {
			return Resolved{}, fmt.Errorf("%s: %q is not a duration", set.Key, set.Value)
		}
		switch set.Key {
		case KeyRiskEvents:
			out.RiskEvents = d.Std()
		case KeyMinuteStats:
			out.MinuteStats = d.Std()
		case KeyHourStats:
			out.HourStats = d.Std()
		case KeyStateEvents:
			out.StateEvents = d.Std()
		case KeyAudit:
			out.Audit = d.Std()
		case KeyAlertEvents:
			out.AlertEvents = d.Std()
		}
	}
	return out, nil
}

// ErrPinned is returned when an update names a setting the environment pins.
var ErrPinned = errors.New("settings: pinned by the environment")

// ErrUnknown is returned when an update names a setting that does not exist.
var ErrUnknown = errors.New("settings: unknown setting")

// Update writes the given settings and returns the full list as it now stands.
//
// Only the keys present in values are touched. An empty value clears the
// database's opinion of that setting, which returns it to its default — the
// reset the console offers. The whole update is validated before anything is
// written, so a request with one bad value changes nothing.
func (s *Store) Update(ctx context.Context, values map[string]string) ([]Setting, error) {
	if s.q == nil {
		return nil, errors.New("settings: no database")
	}
	saved, err := s.load(ctx)
	if err != nil {
		return nil, err
	}

	// Validate everything first.
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sp, ok := specOf(key)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknown, key)
		}
		if s.cfg.FromEnv[key] {
			return nil, fmt.Errorf("%w: %s is set by %s", ErrPinned, key, sp.envVar)
		}
		if values[key] == "" {
			continue // a reset needs no validation
		}
		if err := validate(sp, values[key]); err != nil {
			return nil, err
		}
	}

	for _, key := range keys {
		if f := saved.field(key); f != nil {
			*f = normalize(key, values[key])
		}
	}

	blob, err := json.Marshal(saved)
	if err != nil {
		return nil, fmt.Errorf("encode deployment settings: %w", err)
	}
	if _, err := s.q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{Key: storeKey, Value: blob}); err != nil {
		return nil, fmt.Errorf("write deployment settings: %w", err)
	}
	return s.merge(saved), nil
}

func specOf(key string) (spec, bool) {
	for _, sp := range specs {
		if sp.key == key {
			return sp, true
		}
	}
	return spec{}, false
}

// normalize rewrites an accepted value into its canonical spelling, so that
// "1440m" is stored and shown back as "24h".
func normalize(key, value string) string {
	if value == "" {
		return ""
	}
	if key == KeyClickHouse {
		return value
	}
	d, err := durationx.Parse(value)
	if err != nil {
		return value // unreachable: validated first
	}
	return d.String()
}

func validate(sp spec, value string) error {
	if sp.unit == UnitDays {
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %q is not a whole number of days", sp.key, value)
		}
		if n < MinTTLDays || n > MaxTTLDays {
			return fmt.Errorf("%s: %d days is outside %d-%d", sp.key, n, MinTTLDays, MaxTTLDays)
		}
		return nil
	}
	d, err := durationx.Parse(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not a duration", sp.key, value)
	}
	if d.IsPermanent() {
		return fmt.Errorf("%s: retention cannot be permanent", sp.key)
	}
	if std := d.Std(); std < MinRetention || std > MaxRetention {
		return fmt.Errorf("%s: %s is outside %s-%s", sp.key, d.String(),
			durationx.Duration(MinRetention).String(), durationx.Duration(MaxRetention).String())
	}
	return nil
}
