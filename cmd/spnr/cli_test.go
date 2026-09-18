package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/cmd/internal/buildinfo"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

func TestRootHelpListsCommands(t *testing.T) {
	code, out, _ := runCLI(t, nil, "", "--help")
	require.Equal(t, 0, code)
	for _, cmd := range []string{"migrate", "admin", "token", "rebuild", "kek", "seed", "healthcheck", "version", "config"} {
		require.Contains(t, out, cmd)
	}

	code, out, _ = runCLI(t, nil, "", "version")
	require.Equal(t, 0, code)
	require.Equal(t, "spnr "+buildinfo.Version()+"\n", out)

	code, _, errOut := runCLI(t, nil, "", "does-not-exist")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "unknown command")

	code, _, errOut = runCLI(t, nil, "", "migrate", "up", "extra")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "unknown command")
}

func TestDatabaseCommandsNeedOnlyTheDatabaseURL(t *testing.T) {
	for _, args := range [][]string{
		{"migrate", "up"}, {"migrate", "status"}, {"migrate", "down"},
		{"admin", "init", "--username", "admin", "--password-env", "PW"},
		{"token", "create", "--name", "n", "--scope", "lease:acquire"},
	} {
		code, _, errOut := runCLI(t, map[string]string{"PW": "a-long-password"}, "", args...)
		require.Equalf(t, 1, code, "%v", args)
		require.Containsf(t, errOut, "SPINNERET_DATABASE_URL is required", "%v", args)
		require.NotContainsf(t, errOut, "REDIS", "%v", args)
		require.NotContainsf(t, errOut, "KEK", "%v", args)
	}
	code, _, errOut := runCLI(t, map[string]string{"SPINNERET_DATABASE_URL": "postgres://x", "SPINNERET_DATABASE_MAX_CONNS": "1"}, "", "migrate", "status")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "SPINNERET_DATABASE_MAX_CONNS")
}

func TestAdminInitArguments(t *testing.T) {
	code, _, errOut := runCLI(t, nil, "", "admin", "init", "--password-env", "PW")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, `"username" not set`)

	code, _, errOut = runCLI(t, nil, "", "admin", "init", "--username", "admin")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "password-env")

	code, _, errOut = runCLI(t, nil, "", "admin", "init", "--username", "admin", "--password-env", "PW", "--password-stdin")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "none of the others can be")

	a := &app{lookup: func(k string) (string, bool) {
		if k == "PW" {
			return "from-env-secret", true
		}
		return "", false
	}}
	pw, err := a.readPassword("PW", false)
	require.NoError(t, err)
	require.Equal(t, "from-env-secret", pw)
	_, err = a.readPassword("MISSING", false)
	require.ErrorContains(t, err, "MISSING is not set")

	a.stdin = strings.NewReader("stdin secret \r\nsecond line")
	pw, err = a.readPassword("", true)
	require.NoError(t, err)
	require.Equal(t, "stdin secret ", pw, "only the line terminator is stripped")
	a.stdin = strings.NewReader("no-newline")
	pw, err = a.readPassword("", true)
	require.NoError(t, err)
	require.Equal(t, "no-newline", pw)
	a.stdin = strings.NewReader("\n")
	_, err = a.readPassword("", true)
	require.ErrorContains(t, err, "empty")
	_, err = a.readPassword("", false)
	require.Error(t, err)
}

func TestPublishCatalogInvalidation(t *testing.T) {
	bus := events.NewMemoryBus()
	got := make(chan events.Event, 1)
	unsubscribe := bus.Subscribe(events.ChannelCatalog, func(_ context.Context, _ string, ev events.Event) { got <- ev })
	defer unsubscribe()
	require.NoError(t, publishCatalogInvalidation(context.Background(), bus, "ns_1"))
	select {
	case ev := <-got:
		require.Equal(t, catalog.InvalidateEventType, ev.Type)
		require.Equal(t, "ns_1", ev.NamespaceID)
		require.JSONEq(t, `{"ns":"ns_1"}`, string(ev.Data))
	case <-time.After(5 * time.Second):
		t.Fatal("no catalog event published")
	}
	require.True(t, isAlreadyExists(apperr.AlreadyExists("site exists")))
	require.False(t, isAlreadyExists(errors.New("other")))
}

func TestTokenCreateValidation(t *testing.T) {
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	in := tokenCreateInput{name: "node", scopes: []string{"lease:acquire", "secret:read:default/signer/*"}, expires: "720h"}
	exp, err := in.validate(now)
	require.NoError(t, err)
	require.Equal(t, now.Add(720*time.Hour), *exp)

	for _, tc := range []struct {
		expires string
		want    *time.Time
		err     bool
	}{
		{expires: "0"}, {expires: "never"}, {expires: ""},
		{expires: "30d", want: ptr(now.Add(30 * 24 * time.Hour))},
		{expires: "-1h", err: true}, {expires: "soon", err: true}, {expires: "permanent"},
	} {
		got, err := parseExpiry(tc.expires, now)
		if tc.err {
			require.Errorf(t, err, "%q", tc.expires)
			continue
		}
		require.NoErrorf(t, err, "%q", tc.expires)
		require.Equalf(t, tc.want, got, "%q", tc.expires)
	}

	_, err = (&tokenCreateInput{name: " ", scopes: []string{"lease:acquire"}}).validate(now)
	require.ErrorContains(t, err, "--name")
	_, err = (&tokenCreateInput{name: "n"}).validate(now)
	require.ErrorContains(t, err, "--scope")
	_, err = (&tokenCreateInput{name: "n", scopes: []string{"not-a-scope"}}).validate(now)
	require.ErrorContains(t, err, "invalid --scope")
	_, err = (&tokenCreateInput{name: "n", scopes: []string{"admin"}, description: strings.Repeat("x", 513)}).validate(now)
	require.ErrorContains(t, err, "--description")

	code, _, errOut := runCLI(t, nil, "", "token", "create", "--name", "n")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, `"scope" not set`)
}

func ptr[T any](v T) *T { return &v }

func TestMigrateHelpers(t *testing.T) {
	target, err := downTarget(3, 0, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, target)
	target, err = downTarget(0, 0, false)
	require.NoError(t, err)
	require.Zero(t, target)
	target, err = downTarget(3, 1, true)
	require.NoError(t, err)
	require.EqualValues(t, 1, target)
	_, err = downTarget(3, 4, true)
	require.Error(t, err)

	require.Contains(t, migrationStatus(3, 3), "up to date")
	require.Contains(t, migrationStatus(1, 3), "2 pending")
	require.Contains(t, migrationStatus(4, 3), "newer than this binary")

	code, _, errOut := runCLI(t, map[string]string{"SPINNERET_DATABASE_URL": "postgres://x"}, "", "migrate", "down", "--to", "-1")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "--to must be >= 0")
}

func TestRebuildArguments(t *testing.T) {
	code, _, errOut := runCLI(t, nil, "", "rebuild", "--tenant", "acme")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "add --site")
}

func TestKEKGenerate(t *testing.T) {
	code, out, errOut := runCLI(t, nil, "", "kek", "generate", "--id", "k2")
	require.Equal(t, 0, code, errOut)
	id, key, ok := strings.Cut(strings.TrimSpace(out), ":")
	require.True(t, ok)
	require.Equal(t, "k2", id)
	raw, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err)
	require.Len(t, raw, 32)

	code, _, errOut = runCLI(t, nil, "", "kek", "generate", "--id", "bad id!")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "invalid --id")

	code, _, errOut = runCLI(t, map[string]string{"SPINNERET_DATABASE_URL": "postgres://x"}, "", "kek", "status")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "SPINNERET_KEK_FILE or SPINNERET_KEKS is required")
}

// fakeRewrapper replays a sequence of statuses.
type fakeRewrapper struct {
	started  bool
	startErr error
	statuses []vault.KEKStatus
}

func (f *fakeRewrapper) Start(context.Context) (bool, error) { return f.started, f.startErr }

func (f *fakeRewrapper) Status(context.Context) (vault.KEKStatus, error) {
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return st, nil
}

func TestRunRewrapFollowsProgress(t *testing.T) {
	var out strings.Builder
	r := &fakeRewrapper{started: true, statuses: []vault.KEKStatus{
		{CurrentKEKID: "k2", Running: true, Done: 1, Total: 3},
		{CurrentKEKID: "k2", Running: true, Done: 2, Total: 3},
		{CurrentKEKID: "k2", Done: 3, Total: 3},
	}}
	require.NoError(t, runRewrap(context.Background(), r, &out, time.Millisecond))
	require.Contains(t, out.String(), "kek rewrap started")
	require.Contains(t, out.String(), "progress: 2/3")
	require.Contains(t, out.String(), "kek rewrap finished")

	out.Reset()
	r = &fakeRewrapper{statuses: []vault.KEKStatus{{Done: 1, Total: 2, LastError: "decrypt failed"}}}
	require.ErrorContains(t, runRewrap(context.Background(), r, &out, time.Millisecond), "decrypt failed")
	require.Contains(t, out.String(), "already running on another instance")

	r = &fakeRewrapper{startErr: errors.New("db down")}
	require.ErrorContains(t, runRewrap(context.Background(), r, &out, time.Millisecond), "db down")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = &fakeRewrapper{started: true, statuses: []vault.KEKStatus{{Running: true}}}
	require.ErrorIs(t, runRewrap(ctx, r, &out, time.Hour), context.Canceled)

	out.Reset()
	now := time.Now()
	require.NoError(t, printKEKStatus(&out, vault.KEKStatus{
		CurrentKEKID: "k2", LastFinishedAt: &now, LastError: "x",
		KEKs: []vault.KEKInfo{{ID: "k1", WrappedRecords: 4}, {ID: "k2", Current: true, Configured: true, WrappedRecords: 9}},
	}))
	require.Contains(t, out.String(), "k1               wrapped_records=4 NOT-CONFIGURED")
	require.Contains(t, out.String(), "wrapped_records=9 current")
	require.Contains(t, out.String(), "last error: x")
}

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()

	code, _, errOut := runCLI(t, nil, "", "healthcheck", "--url", srv.URL+"/readyz")
	require.Equal(t, 0, code, errOut)

	status = http.StatusServiceUnavailable
	code, _, errOut = runCLI(t, nil, "", "healthcheck", "--url", srv.URL+"/readyz")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "status 503")

	status = http.StatusFound
	code, _, _ = runCLI(t, nil, "", "healthcheck", "--url", srv.URL)
	require.Equal(t, 1, code, "redirects are not followed")

	code, _, errOut = runCLI(t, nil, "", "healthcheck", "--url", "ftp://host/x")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "absolute http(s) URL")

	code, _, errOut = runCLI(t, nil, "", "healthcheck", "--url", "http://127.0.0.1:1/readyz", "--timeout", "200ms")
	require.Equal(t, 1, code)
	require.NotEmpty(t, errOut)

	code, _, errOut = runCLI(t, nil, "", "healthcheck", "--url", srv.URL, "--timeout", "0s")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "--timeout must be positive")

	// Credentials in the URL never appear in the output.
	code, _, errOut = runCLI(t, nil, "", "healthcheck", "--url", "http://user:hunter2@127.0.0.1:1/", "--timeout", "200ms")
	require.Equal(t, 1, code)
	require.NotContains(t, errOut, "hunter2")
}

func TestConfigCheck(t *testing.T) {
	code, _, errOut := runCLI(t, map[string]string{}, "", "config", "check")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "SPINNERET_DATABASE_URL is required")
	require.Contains(t, errOut, "SPINNERET_KEK_FILE or SPINNERET_KEKS is required")

	code, out, errOut := runCLI(t, map[string]string{
		"SPINNERET_DATABASE_URL": "postgres://spinneret:topsecret@db:5432/spinneret",
		"SPINNERET_REDIS_URL":    "redis://valkey:6379/0",
		"SPINNERET_KEKS":         "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}, "", "config", "check")
	require.Equal(t, 0, code, errOut)
	require.NotContains(t, out, "topsecret")
	require.NotContains(t, out, "AAAAAAAA")
	require.Contains(t, out, `"role": "all"`)
}
