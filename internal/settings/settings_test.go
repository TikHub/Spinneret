package settings

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// fakeQ is the system_settings row, in memory.
type fakeQ struct {
	row     json.RawMessage
	present bool
	writes  int
	getErr  error
}

func (f *fakeQ) GetSystemSetting(context.Context, string) (db.SystemSetting, error) {
	if f.getErr != nil {
		return db.SystemSetting{}, f.getErr
	}
	if !f.present {
		return db.SystemSetting{}, pgx.ErrNoRows
	}
	return db.SystemSetting{Key: storeKey, Value: f.row}, nil
}

func (f *fakeQ) UpsertSystemSetting(_ context.Context, arg db.UpsertSystemSettingParams) (db.SystemSetting, error) {
	f.writes++
	f.row, f.present = arg.Value, true
	return db.SystemSetting{Key: arg.Key, Value: arg.Value}, nil
}

func defaults() Defaults {
	return Defaults{
		RiskEvents:        720 * time.Hour,
		MinuteStats:       720 * time.Hour,
		HourStats:         4320 * time.Hour,
		StateEvents:       8760 * time.Hour,
		Audit:             8760 * time.Hour,
		AlertEvents:       2160 * time.Hour,
		ClickHouseTTLDays: 90,
	}
}

func get(t *testing.T, list []Setting, key string) Setting {
	t.Helper()
	for _, s := range list {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no setting %q", key)
	return Setting{}
}

func TestDefaultsWhenNothingIsSet(t *testing.T) {
	s := New(&fakeQ{}, Config{Defaults: defaults()})

	list, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, len(specs))

	risk := get(t, list, KeyRiskEvents)
	require.Equal(t, "30d", risk.Value)
	require.Equal(t, OriginDefault, risk.Origin)
	require.True(t, risk.Editable())
	require.Equal(t, "90", get(t, list, KeyClickHouse).Value)
}

func TestDatabaseOverridesTheDefault(t *testing.T) {
	q := &fakeQ{}
	s := New(q, Config{Defaults: defaults()})

	_, err := s.Update(context.Background(), map[string]string{KeyRiskEvents: "168h"})
	require.NoError(t, err)

	list, err := s.List(context.Background())
	require.NoError(t, err)
	risk := get(t, list, KeyRiskEvents)
	require.Equal(t, "7d", risk.Value)
	require.Equal(t, OriginDatabase, risk.Origin)
	require.Equal(t, "30d", risk.Default, "the default is still reported, so the console can offer a reset")
	// Untouched settings keep falling through.
	require.Equal(t, OriginDefault, get(t, list, KeyAudit).Origin)
}

func TestEnvironmentWinsAndPins(t *testing.T) {
	// A deployment managed from a file keeps the guarantee that the file is what
	// runs: the console shows the value and refuses to change it.
	d := defaults()
	d.RiskEvents = 48 * time.Hour // what the environment resolved
	s := New(&fakeQ{}, Config{Defaults: d, FromEnv: map[string]bool{KeyRiskEvents: true}})

	risk := get(t, mustList(t, s), KeyRiskEvents)
	require.Equal(t, "2d", risk.Value)
	require.Equal(t, OriginEnvironment, risk.Origin)
	require.False(t, risk.Editable())
	require.Equal(t, "SPINNERET_RETENTION_RISK_EVENTS", risk.EnvVar, "the operator is told which variable to remove")

	_, err := s.Update(context.Background(), map[string]string{KeyRiskEvents: "168h"})
	require.ErrorIs(t, err, ErrPinned)
}

func TestEnvironmentWinsOverAValueAlreadyInTheDatabase(t *testing.T) {
	// The database row survives the variable being added; the variable still wins
	// while it is set, and removing it returns the stored value rather than the
	// default.
	q := &fakeQ{}
	plain := New(q, Config{Defaults: defaults()})
	_, err := plain.Update(context.Background(), map[string]string{KeyAudit: "72h"})
	require.NoError(t, err)

	d := defaults()
	d.Audit = 48 * time.Hour
	pinned := New(q, Config{Defaults: d, FromEnv: map[string]bool{KeyAudit: true}})
	require.Equal(t, "2d", get(t, mustList(t, pinned), KeyAudit).Value)

	require.Equal(t, "3d", get(t, mustList(t, plain), KeyAudit).Value)
}

func TestEmptyValueResetsToTheDefault(t *testing.T) {
	q := &fakeQ{}
	s := New(q, Config{Defaults: defaults()})

	_, err := s.Update(context.Background(), map[string]string{KeyMinuteStats: "48h"})
	require.NoError(t, err)
	require.Equal(t, OriginDatabase, get(t, mustList(t, s), KeyMinuteStats).Origin)

	_, err = s.Update(context.Background(), map[string]string{KeyMinuteStats: ""})
	require.NoError(t, err)
	got := get(t, mustList(t, s), KeyMinuteStats)
	require.Equal(t, "30d", got.Value)
	require.Equal(t, OriginDefault, got.Origin)
}

func TestValuesAreNormalized(t *testing.T) {
	// durationx canonicalises to the largest whole unit, so what an operator
	// types is stored and shown back in one spelling. "720h" and "30d" are the
	// same value; the console shows "30d".
	s := New(&fakeQ{}, Config{Defaults: defaults()})
	list, err := s.Update(context.Background(), map[string]string{KeyStateEvents: "1440m"})
	require.NoError(t, err)
	require.Equal(t, "1d", get(t, list, KeyStateEvents).Value)
}

func TestAnInvalidValueWritesNothing(t *testing.T) {
	// The whole update is validated first, so one bad value in a form submission
	// does not leave the others applied.
	q := &fakeQ{}
	s := New(q, Config{Defaults: defaults()})

	_, err := s.Update(context.Background(), map[string]string{
		KeyRiskEvents: "168h",
		KeyAudit:      "10m", // below the floor
	})
	require.Error(t, err)
	require.Zero(t, q.writes, "nothing is written when any value is rejected")
	require.Equal(t, OriginDefault, get(t, mustList(t, s), KeyRiskEvents).Origin)
}

func TestRejects(t *testing.T) {
	s := New(&fakeQ{}, Config{Defaults: defaults()})
	cases := map[string]map[string]string{
		"below the retention floor":   {KeyAudit: "1h"},
		"above the retention ceiling": {KeyAudit: "100000h"},
		"not a duration":              {KeyAudit: "soon"},
		"permanent":                   {KeyAudit: "permanent"},
		"ttl below 1":                 {KeyClickHouse: "0"},
		"ttl above 3650":              {KeyClickHouse: "4000"},
		"ttl not a number":            {KeyClickHouse: "90d"},
		"unknown key":                 {"retention.nope": "24h"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Update(context.Background(), values)
			require.Error(t, err)
		})
	}
}

func TestResolve(t *testing.T) {
	q := &fakeQ{}
	s := New(q, Config{Defaults: defaults()})
	_, err := s.Update(context.Background(), map[string]string{
		KeyRiskEvents: "168h",
		KeyClickHouse: "14",
	})
	require.NoError(t, err)

	got, err := s.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, 168*time.Hour, got.RiskEvents)
	require.Equal(t, 14, got.ClickHouseTTLDays)
	require.Equal(t, 8760*time.Hour, got.Audit, "untouched settings resolve to their default")
}

func TestAnUnreadableDatabaseIsAnErrorNotASilentDefault(t *testing.T) {
	// Shortening a retention and having it silently not apply is the kind of
	// thing an operator finds out about from a full disk.
	q := &fakeQ{getErr: context.DeadlineExceeded}
	s := New(q, Config{Defaults: defaults()})

	_, err := s.Resolve(context.Background())
	require.Error(t, err)
	_, err = s.List(context.Background())
	require.Error(t, err)
}

func TestACorruptRowDoesNotTakeRetentionWithIt(t *testing.T) {
	q := &fakeQ{present: true, row: json.RawMessage(`{"risk_events":`)}
	s := New(q, Config{Defaults: defaults()})
	_, err := s.List(context.Background())
	require.ErrorContains(t, err, "decode deployment settings")
}

func mustList(t *testing.T, s *Store) []Setting {
	t.Helper()
	list, err := s.List(context.Background())
	require.NoError(t, err)
	return list
}
