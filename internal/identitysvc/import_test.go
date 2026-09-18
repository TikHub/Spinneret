package identitysvc_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/identitysvc"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvctest"
)

// storedIdentity is the database state of an identity checked by tests.
type storedIdentity struct {
	ID              string
	State           string
	StateReason     string
	PayloadVersion  int
	AccountID       *string
	Region          string
	Tags            []string
	QuarantineUntil *time.Time
	ActivatedAt     *time.Time
}

func loadIdentities(t *testing.T, env *identitysvctest.Env, typeID string) map[string]storedIdentity {
	t.Helper()
	rows, err := env.Pool.Query(context.Background(), `SELECT i.id, i.state, i.state_reason, i.payload_version, i.account_id,
		i.region, i.tags, i.quarantine_until, i.activated_at, coalesce(a.external_ref, i.id)
		FROM identities i LEFT JOIN accounts a ON a.id = i.account_id WHERE i.type_id = $1`, typeID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]storedIdentity{}
	for rows.Next() {
		var s storedIdentity
		var key string
		require.NoError(t, rows.Scan(&s.ID, &s.State, &s.StateReason, &s.PayloadVersion, &s.AccountID, &s.Region, &s.Tags,
			&s.QuarantineUntil, &s.ActivatedAt, &key))
		out[s.ID] = s
	}
	require.NoError(t, rows.Err())
	return out
}

func importJSONL(t *testing.T, env *identitysvctest.Env, typeName, data string, mutate ...func(*identitysvc.ImportInput)) identitysvc.ImportResult {
	t.Helper()
	in := identitysvc.ImportInput{Site: "shop", Type: typeName, Format: "jsonl", Data: data}
	for _, m := range mutate {
		m(&in)
	}
	res, err := env.Service.ImportIdentities(context.Background(), env.Role(authz.RoleOperator), env.NS, in)
	require.NoError(t, err)
	return res
}

func identityBySession(t *testing.T, env *identitysvctest.Env, session string) string {
	t.Helper()
	page, err := env.Service.ListIdentities(context.Background(), env.Owner(env.NS), env.NS, identitysvc.IdentityQuery{
		Filter: identitysvc.IdentityFilter{Search: session, IncludeRetired: true},
	})
	require.NoError(t, err)
	require.Len(t, page.Identities, 1)
	return page.Identities[0].ID
}

func TestImportCreatesAndDeduplicates(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	app := env.CreateType(t, env.SiteA, identitysvctest.AppDeviceYAML)

	data := strings.Join([]string{
		`{"cookies":"sessionid=s1; a=1","user_agent":"UA","_account":"alice","_region":"US","_tags":["vip","fast"],"_labels":{"session":"s1"}}`,
		`{"payload":{"cookies":{"sessionid":"s2"}},"account":"alice","labels":{"session":"s2"}}`,
		``,
		`{"cookies":"sessionid=s1; b=2"}`,
		`not json`,
		`{"user_agent":"missing cookies"}`,
		`{"cookies":"sessionid=s3","_tags":["bad tag!"]}`,
		`{"cookies":"sessionid=s4","_labels":{"bad key!":"x"}}`,
		`{"cookies":"sessionid=s5","_account":"bob","_labels":{"session":"s5"}}`,
	}, "\n")
	res := importJSONL(t, env, "web_cookie", data)
	require.Equal(t, 3, res.Created)
	require.Equal(t, 0, res.Updated)
	require.Equal(t, 0, res.Unchanged)
	lines := make([]int, 0, len(res.Failed))
	for _, f := range res.Failed {
		lines = append(lines, f.Line)
	}
	require.Equal(t, []int{4, 5, 6, 7, 8}, lines)
	require.Contains(t, res.Failed[0].Message, "duplicate unique key (same identity as line 1)")
	require.Contains(t, res.Failed[2].Message, "required field is missing")

	stored := loadIdentities(t, env, web.ID)
	require.Len(t, stored, 3)
	s1 := stored[identityBySession(t, env, "s1")]
	require.Equal(t, identitysvc.StatePending, s1.State)
	require.Equal(t, 1, s1.PayloadVersion)
	require.Equal(t, "US", s1.Region)
	require.Equal(t, []string{"vip", "fast"}, s1.Tags)
	require.NotNil(t, s1.AccountID)
	s2 := stored[identityBySession(t, env, "s2")]
	require.Equal(t, *s1.AccountID, *s2.AccountID, "identities share the account")
	require.Nil(t, s1.ActivatedAt)
	require.Equal(t, 2, env.QueryInt(t, `SELECT count(*) FROM accounts WHERE site_id = $1`, env.SiteA.ID))

	// Hot state: created identities reset, accounts synchronized.
	reset := env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true})
	require.Len(t, reset, 3)
	var accountCalls int
	for _, c := range env.Hot.Calls() {
		if c.Method == "accounts" {
			accountCalls++
			require.Len(t, c.IDs, 2)
		}
	}
	require.Equal(t, 1, accountCalls)
	entry, ok := env.Audit.Last("identity.import")
	require.True(t, ok)
	require.Equal(t, 3, entry.Details["created"])
	require.Equal(t, 5, entry.Details["failed"])

	t.Run("immediate activation", func(t *testing.T) {
		res := importJSONL(t, env, "app_device", "{\"device_id\":\"d1\",\"install_id\":\"i1\"}\n")
		require.Equal(t, 1, res.Created)
		for _, s := range loadIdentities(t, env, app.ID) {
			require.Equal(t, identitysvc.StateActive, s.State)
			require.NotNil(t, s.ActivatedAt)
		}
	})

	t.Run("csv", func(t *testing.T) {
		csv := "device_id,install_id,_account,_region,_tags,_labels\nd2,i2,carol,JP,a;b,session=d2\nd3,,,,,\n"
		res, err := env.Service.ImportIdentities(ctx, env.Role(authz.RoleOperator), env.NS, identitysvc.ImportInput{
			Site: "shop", Type: "app_device", Format: "csv", Data: csv,
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.Created)
		require.Len(t, res.Failed, 1)
		require.Equal(t, 3, res.Failed[0].Line)
		require.Len(t, loadIdentities(t, env, app.ID), 2)
	})

	t.Run("re-import is unchanged and applies attributes", func(t *testing.T) {
		env.Hot.Reset()
		res := importJSONL(t, env, "web_cookie", `{"cookies":"a=1; sessionid=s1","user_agent":"UA","_region":"DE","_labels":{"session":"s1"}}`)
		require.Equal(t, identitysvc.ImportResult{Unchanged: 1, Failed: []identitysvc.ImportFailure{}}, res)
		s1 := loadIdentities(t, env, web.ID)[identityBySession(t, env, "s1")]
		require.Equal(t, "DE", s1.Region)
		require.Equal(t, 1, s1.PayloadVersion)
		require.Equal(t, []string{s1.ID}, env.Hot.SyncedIDs(identitysvc.SyncOptions{}))
		require.Empty(t, env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true}))

		env.Hot.Reset()
		res = importJSONL(t, env, "web_cookie", `{"cookies":"a=1; sessionid=s1","user_agent":"UA","_region":"DE"}`)
		require.Equal(t, 1, res.Unchanged)
		require.Empty(t, env.Hot.SyncedIDs(identitysvc.SyncOptions{}), "no attribute change, no sync")
	})

	t.Run("create_only skips existing identities", func(t *testing.T) {
		res := importJSONL(t, env, "web_cookie", "{\"cookies\":\"sessionid=s1; changed=1\"}\n{\"cookies\":\"sessionid=s9\"}",
			func(in *identitysvc.ImportInput) { in.Mode = identitysvc.ImportModeCreateOnly })
		require.Equal(t, 1, res.Created)
		require.Equal(t, 1, res.Unchanged)
		s1 := loadIdentities(t, env, web.ID)[identityBySession(t, env, "s1")]
		require.Equal(t, 1, s1.PayloadVersion)
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		before := env.QueryInt(t, `SELECT count(*) FROM identities`)
		payloads := env.QueryInt(t, `SELECT count(*) FROM identity_payloads`)
		env.Hot.Reset()
		auditCount := len(env.Audit.Entries())
		res := importJSONL(t, env, "web_cookie", "{\"cookies\":\"sessionid=s1; x=2\"}\n{\"cookies\":\"sessionid=new1\",\"_account\":\"zed\"}\n{\"cookies\":\"sessionid=s2\"}\n{}",
			func(in *identitysvc.ImportInput) { in.DryRun = true })
		require.Equal(t, 1, res.Created)
		require.Equal(t, 1, res.Updated)
		require.Equal(t, 1, res.Unchanged)
		require.Len(t, res.Failed, 1)
		require.Equal(t, before, env.QueryInt(t, `SELECT count(*) FROM identities`))
		require.Equal(t, payloads, env.QueryInt(t, `SELECT count(*) FROM identity_payloads`))
		require.Equal(t, 0, env.QueryInt(t, `SELECT count(*) FROM accounts WHERE external_ref = 'zed'`))
		require.Empty(t, env.Hot.Calls())
		require.Len(t, env.Audit.Entries(), auditCount)
	})
}

func TestImportPayloadUpdatesAndTransitions(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	states := []string{
		identitysvc.StateActive, identitysvc.StatePending, identitysvc.StateQuarantined, identitysvc.StateExpired,
		identitysvc.StateBanned, identitysvc.StateDisabled, identitysvc.StateRetired,
	}
	var rows []string
	for _, st := range states {
		rows = append(rows, fmt.Sprintf(`{"cookies":"sessionid=%s","_labels":{"session":"%s"}}`, st, st))
	}
	res := importJSONL(t, env, "web_cookie", strings.Join(rows, "\n"))
	require.Equal(t, len(states), res.Created)
	ids := map[string]string{}
	for _, st := range states {
		ids[st] = identityBySession(t, env, st)
		env.Exec(t, `UPDATE identities SET state = $1, quarantine_until = CASE WHEN $1 = 'quarantined' THEN now() + interval '1 hour' END WHERE id = $2`, st, ids[st])
	}

	env.Hot.Reset()
	rows = rows[:0]
	for _, st := range states {
		rows = append(rows, fmt.Sprintf(`{"cookies":"sessionid=%s; refreshed=1","_labels":{"session":"%s"}}`, st, st))
	}
	res = importJSONL(t, env, "web_cookie", strings.Join(rows, "\n"))
	require.Equal(t, len(states), res.Updated)

	stored := loadIdentities(t, env, web.ID)
	want := map[string]string{
		identitysvc.StateActive: identitysvc.StatePending, identitysvc.StatePending: identitysvc.StatePending,
		identitysvc.StateQuarantined: identitysvc.StatePending, identitysvc.StateExpired: identitysvc.StatePending,
		identitysvc.StateBanned: identitysvc.StateBanned, identitysvc.StateDisabled: identitysvc.StateDisabled,
		identitysvc.StateRetired: identitysvc.StateRetired,
	}
	for from, to := range want {
		s := stored[ids[from]]
		require.Equal(t, to, s.State, "from %s", from)
		require.Equal(t, 2, s.PayloadVersion)
		if from != to {
			require.Equal(t, "payload updated", s.StateReason)
		}
	}
	require.Nil(t, stored[ids[identitysvc.StateQuarantined]].QuarantineUntil)
	require.ElementsMatch(t, mapValues(ids), env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true}))

	// Three transitions: active, quarantined and expired identities became pending.
	require.Equal(t, 3, env.QueryInt(t, `SELECT count(*) FROM state_events WHERE action = 'payload_update'`))
	var stateEvents []events.Event
	for _, ev := range env.Events.All() {
		if ev.Type == events.TypeIdentityState {
			stateEvents = append(stateEvents, ev)
		}
	}
	require.Len(t, stateEvents, 3)
	data := identitysvctest.Data(stateEvents[0])
	require.Equal(t, "identity", data["subject_kind"])
	require.Equal(t, "pending", data["to"])
	require.Equal(t, "payload_update", data["action"])
	require.Equal(t, env.SiteA.ID, stateEvents[0].SiteID)

	t.Run("versions are pruned to the last five", func(t *testing.T) {
		id := ids[identitysvc.StateActive]
		for v := 3; v <= 8; v++ {
			importJSONL(t, env, "web_cookie", fmt.Sprintf(`{"cookies":"sessionid=active; refreshed=%d"}`, v))
		}
		rows, err := env.Pool.Query(ctx, `SELECT version FROM identity_payloads WHERE identity_id = $1 ORDER BY version`, id)
		require.NoError(t, err)
		var versions []int
		for rows.Next() {
			var v int
			require.NoError(t, rows.Scan(&v))
			versions = append(versions, v)
		}
		rows.Close()
		require.Equal(t, []int{4, 5, 6, 7, 8}, versions)

		detail, err := env.Service.GetIdentity(ctx, env.Owner(env.NS), id, true)
		require.NoError(t, err)
		require.True(t, detail.Revealed)
		require.Equal(t, map[string]any{"sessionid": "active", "refreshed": "8"}, detail.Payload["cookies"])
	})

	t.Run("immediate activation re-activates", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.WebCookieYAML, "activation: probe", "activation: immediate", 1)
		updated, err := env.Service.UpdateIdentityType(ctx, env.Owner(env.NS), web.ID, spec)
		require.NoError(t, err)
		env.RegisterType(t, env.SiteA, updated)
		res := importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=expired; refreshed=2"}`)
		require.Equal(t, 1, res.Updated)
		s := loadIdentities(t, env, web.ID)[ids[identitysvc.StateExpired]]
		require.Equal(t, identitysvc.StateActive, s.State)
		require.NotNil(t, s.ActivatedAt)
	})
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestImportLimitsAndErrors(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.AppDeviceYAML)
	op := env.Role(authz.RoleOperator)
	base := identitysvc.ImportInput{Site: "shop", Type: "app_device", Format: "jsonl", Data: `{"device_id":"d","install_id":"i"}`}

	tests := []struct {
		name   string
		p      *authz.Principal
		mutate func(*identitysvc.ImportInput)
		reason apperr.Reason
	}{
		{"viewer", env.Role(authz.RoleViewer), nil, apperr.ReasonPermissionDenied},
		{"token scoped to another site", env.Token(t, "identity:write:market"), nil, apperr.ReasonScopeMissing},
		{"unknown site", op, func(in *identitysvc.ImportInput) { in.Site = "nope" }, apperr.ReasonSiteUnknown},
		{"unknown type", op, func(in *identitysvc.ImportInput) { in.Type = "nope" }, apperr.ReasonNotFound},
		{"missing type", op, func(in *identitysvc.ImportInput) { in.Type = "" }, apperr.ReasonInvalidArgument},
		{"bad mode", op, func(in *identitysvc.ImportInput) { in.Mode = "merge" }, apperr.ReasonInvalidArgument},
		{"bad format", op, func(in *identitysvc.ImportInput) { in.Format = "xml" }, apperr.ReasonInvalidArgument},
		{"too many bytes", op, func(in *identitysvc.ImportInput) {
			in.Data = strings.Repeat(" ", int(identitysvc.ImportMaxBytes)+1)
		}, apperr.ReasonInvalidArgument},
		{"too many rows", op, func(in *identitysvc.ImportInput) {
			in.Data = strings.Repeat("{}\n", identitysvc.ImportMaxRows+1)
		}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			_, err := env.Service.ImportIdentities(ctx, tc.p, env.NS, in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("site scoped token may import", func(t *testing.T) {
		res, err := env.Service.ImportIdentities(ctx, env.Token(t, "identity:write:shop"), env.NS, base)
		require.NoError(t, err)
		require.Equal(t, 1, res.Created)
	})

	t.Run("failures are capped", func(t *testing.T) {
		in := base
		in.Data = strings.Repeat("{\"device_id\":\"x\"}\n", identitysvc.MaxImportFailures+5)
		res, err := env.Service.ImportIdentities(ctx, op, env.NS, in)
		require.NoError(t, err)
		require.Len(t, res.Failed, identitysvc.MaxImportFailures+1)
		last := res.Failed[len(res.Failed)-1]
		require.Equal(t, 0, last.Line)
		require.Equal(t, "5 more rows failed", last.Message)
	})

	t.Run("many rows span several chunks", func(t *testing.T) {
		n := identitysvc.ImportChunkSize*2 + 37
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "{\"device_id\":\"bulk-%d\",\"install_id\":\"i\",\"_account\":\"acct-%d\"}\n", i, i%7)
		}
		in := base
		in.Data = b.String()
		env.Hot.Reset()
		res, err := env.Service.ImportIdentities(ctx, op, env.NS, in)
		require.NoError(t, err)
		require.Equal(t, n, res.Created)
		require.Empty(t, res.Failed)
		require.Len(t, env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true}), n)
		require.Equal(t, 7, env.QueryInt(t, `SELECT count(*) FROM accounts WHERE external_ref LIKE 'acct-%'`))

		res, err = env.Service.ImportIdentities(ctx, op, env.NS, in)
		require.NoError(t, err)
		require.Equal(t, n, res.Unchanged)
	})

	t.Run("concurrent imports of overlapping keys", func(t *testing.T) {
		var b strings.Builder
		for i := range 300 {
			fmt.Fprintf(&b, "{\"device_id\":\"race-%d\",\"install_id\":\"i\",\"_account\":\"racer-%d\"}\n", i, i%11)
		}
		in := base
		in.Data = b.String()
		const workers = 4
		errs := make(chan error, workers)
		results := make(chan identitysvc.ImportResult, workers)
		for range workers {
			go func() {
				res, err := env.Service.ImportIdentities(ctx, op, env.NS, in)
				errs <- err
				results <- res
			}()
		}
		created, unchanged := 0, 0
		for range workers {
			require.NoError(t, <-errs)
			res := <-results
			created += res.Created
			unchanged += res.Unchanged
		}
		require.Equal(t, 300, created)
		require.Equal(t, 300*(workers-1), unchanged)
		require.Equal(t, 300, env.QueryInt(t, `SELECT count(*) FROM identities i JOIN accounts a ON a.id = i.account_id WHERE a.external_ref LIKE 'racer-%'`))
		require.Equal(t, 11, env.QueryInt(t, `SELECT count(*) FROM accounts WHERE external_ref LIKE 'racer-%'`))
	})

	t.Run("hot sync failure does not fail the import", func(t *testing.T) {
		env.Hot.Err = fmt.Errorf("redis down")
		defer func() { env.Hot.Err = nil }()
		in := base
		in.Data = `{"device_id":"sync-fail","install_id":"i"}`
		res, err := env.Service.ImportIdentities(ctx, op, env.NS, in)
		require.NoError(t, err)
		require.Equal(t, 1, res.Created)
		entry, ok := env.Audit.Last("identity.import")
		require.True(t, ok)
		require.Equal(t, true, entry.Details["hot_sync_failed"])
	})

	t.Run("canceled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		in := base
		in.Data = `{"device_id":"canceled","install_id":"i"}`
		_, err := env.Service.ImportIdentities(cctx, op, env.NS, in)
		require.Error(t, err)
		require.Zero(t, env.QueryInt(t, `SELECT count(*) FROM identities WHERE labels->>'k' = 'canceled'`))
	})

	t.Run("failing chunk keeps earlier chunks and audits the error", func(t *testing.T) {
		env.Exec(t, `CREATE FUNCTION fail_boom() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.labels->>'k' = 'boom' THEN RAISE EXCEPTION 'boom'; END IF; RETURN NEW; END $$`)
		env.Exec(t, `CREATE TRIGGER fail_boom BEFORE INSERT ON identities FOR EACH ROW EXECUTE FUNCTION fail_boom()`)
		defer env.Exec(t, `DROP FUNCTION fail_boom() CASCADE`)
		var b strings.Builder
		for i := range identitysvc.ImportChunkSize + 10 {
			label := fmt.Sprintf("chunk-%d", i)
			if i == identitysvc.ImportChunkSize+5 {
				label = "boom"
			}
			fmt.Fprintf(&b, "{\"device_id\":\"chunk-%d\",\"install_id\":\"i\",\"_labels\":{\"k\":%q}}\n", i, label)
		}
		in := base
		in.Data = b.String()
		env.Hot.Reset()
		_, err := env.Service.ImportIdentities(ctx, op, env.NS, in)
		require.Error(t, err)
		_, isAppErr := apperr.As(err)
		require.False(t, isAppErr, "infrastructure failures surface as internal errors: %v", err)
		require.Equal(t, identitysvc.ImportChunkSize, env.QueryInt(t, `SELECT count(*) FROM identities WHERE labels->>'k' LIKE 'chunk-%'`))
		require.Len(t, env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true}), identitysvc.ImportChunkSize,
			"committed chunks are synchronized")
		entry, ok := env.Audit.Last("identity.import")
		require.True(t, ok)
		require.Equal(t, "error", entry.Result)
		require.Equal(t, identitysvc.ImportChunkSize, entry.Details["created"])
		require.Equal(t, "internal", entry.Details["error"])
	})

	t.Run("missing pepper", func(t *testing.T) {
		svc := identitysvc.NewService(env.Pool, env.Cipher, []byte("short"), env.Catalog, env.Hot, env.Ops, nil, nil, nil)
		_, err := svc.ImportIdentities(ctx, op, env.NS, base)
		requireReason(t, err, apperr.ReasonInternal)
		svc = identitysvc.NewService(env.Pool, nil, env.Pepper, env.Catalog, env.Hot, env.Ops, nil, nil, nil)
		_, err = svc.ImportIdentities(ctx, op, env.NS, base)
		requireReason(t, err, apperr.ReasonInternal)
		svc = identitysvc.NewService(nil, env.Cipher, env.Pepper, env.Catalog, env.Hot, env.Ops, nil, nil, nil)
		_, err = svc.ImportIdentities(ctx, op, env.NS, base)
		requireReason(t, err, apperr.ReasonInternal)
	})
}
