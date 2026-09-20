package systemapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/settings"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// fakeQ is the system_settings row, in memory.
type fakeQ struct {
	row     json.RawMessage
	present bool
}

func (f *fakeQ) GetSystemSetting(context.Context, string) (db.SystemSetting, error) {
	if !f.present {
		return db.SystemSetting{}, pgx.ErrNoRows
	}
	return db.SystemSetting{Value: f.row}, nil
}

func (f *fakeQ) UpsertSystemSetting(_ context.Context, arg db.UpsertSystemSettingParams) (db.SystemSetting, error) {
	f.row, f.present = arg.Value, true
	return db.SystemSetting{Value: arg.Value}, nil
}

// recorder captures audit entries.
type recorder struct{ entries []audit.Entry }

func (r *recorder) Record(_ context.Context, e audit.Entry) { r.entries = append(r.entries, e) }

func newHandler(t *testing.T) (*Handler, *recorder) {
	t.Helper()
	rec := &recorder{}
	store := settings.New(&fakeQ{}, settings.Config{Defaults: settings.Defaults{
		RiskEvents:        720 * time.Hour,
		MinuteStats:       720 * time.Hour,
		HourStats:         4320 * time.Hour,
		StateEvents:       8760 * time.Hour,
		Audit:             8760 * time.Hour,
		AlertEvents:       2160 * time.Hour,
		ClickHouseTTLDays: 90,
	}})
	return New(nil, store, nil, rec, slog.New(slog.DiscardHandler)), rec
}

func ctxAs(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

var (
	platformAdmin = &authz.Principal{Kind: authz.KindUser, ID: "usr_pa", IsPlatformAdmin: true}
	plainUser     = &authz.Principal{Kind: authz.KindUser, ID: "usr_1"}
	nodeToken     = &authz.Principal{Kind: authz.KindToken, ID: "tok_1"}
)

func TestListSettingsIsOpenToAnyConsoleSession(t *testing.T) {
	// What a deployment keeps its data for is not secret, and an operator looking
	// at a filling disk should not need a role binding to read it.
	h, _ := newHandler(t)

	res, err := h.ListSettings(ctxAs(plainUser), connect.NewRequest(&spinneretv1.ListSettingsRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetSettings())
	require.False(t, res.Msg.GetCanEdit(), "reading is not editing")

	res, err = h.ListSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.ListSettingsRequest{}))
	require.NoError(t, err)
	require.True(t, res.Msg.GetCanEdit())
}

func TestNodeTokensAreRefusedEverywhere(t *testing.T) {
	h, _ := newHandler(t)

	_, err := h.ListSettings(ctxAs(nodeToken), connect.NewRequest(&spinneretv1.ListSettingsRequest{}))
	requireCode(t, err, connect.CodePermissionDenied)

	_, err = h.UpdateSettings(ctxAs(nodeToken), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d"},
	}))
	requireCode(t, err, connect.CodePermissionDenied)
}

func TestOnlyAPlatformAdminMayChangeSettings(t *testing.T) {
	// Deployment-wide and destructive: shortening a retention drops partitions on
	// the next hourly pass, which is not a tenant-level decision.
	h, rec := newHandler(t)

	_, err := h.UpdateSettings(ctxAs(plainUser), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d"},
	}))
	requireCode(t, err, connect.CodePermissionDenied)
	require.Empty(t, rec.entries, "a refusal at the door is not a settings change")

	res, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d"},
	}))
	require.NoError(t, err)
	require.Equal(t, "30d", valueOf(t, res.Msg.GetSettings(), settings.KeyAudit))
	require.Equal(t, spinneretv1.SettingOrigin_SETTING_ORIGIN_DATABASE,
		originOf(t, res.Msg.GetSettings(), settings.KeyAudit))
}

func TestEveryChangeIsAudited(t *testing.T) {
	// Retention decides how long evidence is kept, so the change to it is itself
	// evidence.
	h, rec := newHandler(t)

	_, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d", settings.KeyRiskEvents: ""},
	}))
	require.NoError(t, err)
	require.Len(t, rec.entries, 1)
	e := rec.entries[0]
	require.Equal(t, "settings.update", e.Action)
	require.Equal(t, audit.ResultOK, e.Result)
	// audit_logs has CHECK (result IN ('ok','denied','error')), and the writer is
	// asynchronous, so a value outside that set is not an error the caller sees —
	// the batch is rejected and the entry is dropped with only a log line. A
	// literal here once put "allowed" in this field and lost every successful
	// change; assert the permitted set, not a string.
	require.Contains(t, []string{audit.ResultOK, audit.ResultDenied, audit.ResultError}, e.Result)
	require.Equal(t, "30d", e.Details[settings.KeyAudit])
	require.Equal(t, "(reset to default)", e.Details[settings.KeyRiskEvents], "a reset is a change worth recording")

	// A rejected change is recorded too, with the attempt visible.
	_, err = h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "1h"},
	}))
	require.Error(t, err)
	require.Len(t, rec.entries, 2)
	require.Equal(t, audit.ResultDenied, rec.entries[1].Result)
}

func TestARejectedValueIsAnInvalidArgumentThatNamesIt(t *testing.T) {
	h, _ := newHandler(t)

	_, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "1h"},
	}))
	requireCode(t, err, connect.CodeInvalidArgument)
	require.ErrorContains(t, err, settings.KeyAudit, "the console shows this message, so it has to say which field")

	_, err = h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{}))
	requireCode(t, err, connect.CodeInvalidArgument) // "an empty request changes nothing and says so"
}

func TestAPinnedSettingIsReportedAndRefused(t *testing.T) {
	rec := &recorder{}
	store := settings.New(&fakeQ{}, settings.Config{
		Defaults: settings.Defaults{Audit: 48 * time.Hour, AlertEvents: 2160 * time.Hour, ClickHouseTTLDays: 90},
		FromEnv:  map[string]bool{settings.KeyAudit: true},
	})
	h := New(nil, store, nil, rec, slog.New(slog.DiscardHandler))

	res, err := h.ListSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.ListSettingsRequest{}))
	require.NoError(t, err)
	require.Equal(t, spinneretv1.SettingOrigin_SETTING_ORIGIN_ENVIRONMENT,
		originOf(t, res.Msg.GetSettings(), settings.KeyAudit))

	_, err = h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d"},
	}))
	requireCode(t, err, connect.CodeInvalidArgument)
	require.ErrorContains(t, err, "SPINNERET_RETENTION_AUDIT", "the operator is told which variable pins it")
}

// requireCode asserts the connect code an apperr carries. Handlers return apperr
// values and an interceptor turns them into connect errors, so connect.CodeOf on
// the raw error would report Unknown for every one of them.
func requireCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.Truef(t, ok, "not an apperr: %v", err)
	require.Equal(t, want, e.Code)
}

func TestChangingTheClickHouseRetentionAltersTheTables(t *testing.T) {
	// Storing the number is not applying it. ClickHouse holds the TTL on the tables
	// and is the only thing that enforces it, so a setting that only writes a row
	// displays a retention the deployment does not have — and the disk keeps
	// growing at the old rate until it is full. That is what this shipped as.
	var applied []int
	store := settings.New(&fakeQ{}, settings.Config{Defaults: settings.Defaults{
		AlertEvents: 2160 * time.Hour, ClickHouseTTLDays: 90,
	}})
	h := New(nil, store, func(_ context.Context, days int) error {
		applied = append(applied, days)
		return nil
	}, &recorder{}, slog.New(slog.DiscardHandler))

	_, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyClickHouse: "30"},
	}))
	require.NoError(t, err)
	require.Equal(t, []int{30}, applied)

	// Clearing it applies whatever it fell back to, not nothing.
	_, err = h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyClickHouse: ""},
	}))
	require.NoError(t, err)
	require.Equal(t, []int{30, 90}, applied)

	// An update that does not mention it does not touch the tables.
	_, err = h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyAudit: "30d"},
	}))
	require.NoError(t, err)
	require.Equal(t, []int{30, 90}, applied)
}

func TestAFailedAlterIsReportedRatherThanSilentlyStored(t *testing.T) {
	// The row is already written when the ALTER fails, so the caller has to hear
	// about it: the alternative is a console that says 30 days over tables that
	// still hold 90.
	store := settings.New(&fakeQ{}, settings.Config{Defaults: settings.Defaults{
		AlertEvents: 2160 * time.Hour, ClickHouseTTLDays: 90,
	}})
	h := New(nil, store, func(context.Context, int) error {
		return errors.New("clickhouse is unreachable")
	}, &recorder{}, slog.New(slog.DiscardHandler))

	_, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyClickHouse: "30"},
	}))
	requireCode(t, err, connect.CodeInternal)
	require.ErrorContains(t, err, "apply the ClickHouse retention")
}

func TestClickHouseDisabledStoresWithoutApplying(t *testing.T) {
	// A deployment without ClickHouse has nothing holding a TTL; the setting still
	// records what it should be for whenever one is connected.
	h, _ := newHandler(t) // constructed with a nil apply callback
	_, err := h.UpdateSettings(ctxAs(platformAdmin), connect.NewRequest(&spinneretv1.UpdateSettingsRequest{
		Values: map[string]string{settings.KeyClickHouse: "30"},
	}))
	require.NoError(t, err)
}

func find(t *testing.T, list []*spinneretv1.Setting, key string) *spinneretv1.Setting {
	t.Helper()
	for _, s := range list {
		if s.GetKey() == key {
			return s
		}
	}
	t.Fatalf("no setting %q", key)
	return nil
}

func valueOf(t *testing.T, list []*spinneretv1.Setting, key string) string {
	t.Helper()
	return find(t, list, key).GetValue()
}

func originOf(t *testing.T, list []*spinneretv1.Setting, key string) spinneretv1.SettingOrigin {
	t.Helper()
	return find(t, list, key).GetOrigin()
}
