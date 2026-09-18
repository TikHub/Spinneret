package vault_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

func TestSecretLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.admin()
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)

	created, err := f.store.Create(ctx, admin, f.ns, vault.CreateSecretInput{
		Path: "signing/api_key", Value: "sk-live-0123456789", Description: "signer",
		Tags: []string{"signing", "prod", "signing"}, ExpiresAt: &expires,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.ID, "sec_"))
	require.Equal(t, 1, created.CurrentVersion)
	require.Equal(t, vault.SecretMask+"6789", created.MaskedValue)
	require.Equal(t, []string{"signing", "prod"}, created.Tags)
	require.Equal(t, admin.Actor(), created.CreatedBy)
	require.Equal(t, "prod", created.NamespaceName)

	got, err := f.store.Get(ctx, f.viewer(), created.ID)
	require.NoError(t, err)
	require.Equal(t, created.MaskedValue, got.MaskedValue)
	require.Equal(t, "signer", got.Description)
	require.Equal(t, f.tenant, got.TenantID)
	require.NotNil(t, got.ExpiresAt)
	require.True(t, expires.Equal(*got.ExpiresAt))

	// A new value creates version 2.
	updated, err := f.store.Update(ctx, admin, created.ID, vault.UpdateSecretInput{Value: ptr("sk-live-new-value-wxyz")})
	require.NoError(t, err)
	require.Equal(t, 2, updated.CurrentVersion)
	require.Equal(t, vault.SecretMask+"wxyz", updated.MaskedValue)
	require.Equal(t, "signer", updated.Description)
	require.Equal(t, []string{"signing", "prod"}, updated.Tags)

	// Metadata-only update keeps the version.
	updated, err = f.store.Update(ctx, admin, created.ID, vault.UpdateSecretInput{
		Description: ptr("rotated"), Tags: nil, SetTags: true, ClearExpiresAt: true,
	})
	require.NoError(t, err)
	require.Equal(t, 2, updated.CurrentVersion)
	require.Equal(t, "rotated", updated.Description)
	require.Empty(t, updated.Tags)
	require.Nil(t, updated.ExpiresAt)
	require.Equal(t, vault.SecretMask+"wxyz", updated.MaskedValue)

	newExpiry := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	updated, err = f.store.Update(ctx, admin, created.ID, vault.UpdateSecretInput{ExpiresAt: &newExpiry})
	require.NoError(t, err)
	require.True(t, newExpiry.Equal(*updated.ExpiresAt))

	versions, err := f.store.ListVersions(ctx, f.viewer(), created.ID, vault.ListVersionsOptions{})
	require.NoError(t, err)
	require.EqualValues(t, 2, versions.Total)
	require.False(t, versions.HasMore)
	require.Len(t, versions.Versions, 2)
	require.Equal(t, 2, versions.Versions[0].Version)
	require.Equal(t, 1, versions.Versions[1].Version)
	require.Equal(t, vaulttest.DefaultKEKID, versions.Versions[0].KEKID)
	require.Equal(t, admin.Actor(), versions.Versions[0].CreatedBy)

	page, err := f.store.ListVersions(ctx, f.viewer(), created.ID, vault.ListVersionsOptions{Limit: 1})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, 2, page.Versions[0].Version)
	page, err = f.store.ListVersions(ctx, f.viewer(), created.ID, vault.ListVersionsOptions{Limit: 1, BeforeVersion: 2})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, 1, page.Versions[0].Version)

	for _, tc := range []struct {
		version, want int
		value         string
	}{
		{0, 2, "sk-live-new-value-wxyz"},
		{1, 1, "sk-live-0123456789"},
		{2, 2, "sk-live-new-value-wxyz"},
	} {
		v, err := f.store.Reveal(ctx, admin, created.ID, tc.version)
		require.NoError(t, err)
		require.Equal(t, tc.want, v.Version)
		require.Equal(t, tc.value, v.Value)
		require.Equal(t, "signing/api_key", v.Path)
	}
	_, err = f.store.Reveal(ctx, admin, created.ID, 3)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.store.Reveal(ctx, admin, created.ID, -1)
	requireReason(t, err, apperr.ReasonInvalidArgument)

	reveals := f.rec.byAction(vault.AuditActionSecretReveal)
	require.Len(t, reveals, 3)
	require.Equal(t, audit.ResultOK, reveals[1].Result)
	require.Equal(t, 1, reveals[1].Details["version"])
	require.Equal(t, created.ID, reveals[1].ResourceID)
	require.Equal(t, "10.0.0.1", reveals[1].IP)

	require.NoError(t, f.store.Delete(ctx, admin, created.ID))
	_, err = f.store.Get(ctx, admin, created.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	err = f.store.Delete(ctx, admin, created.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	var versionsLeft int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM secret_versions WHERE secret_id = $1`, created.ID).Scan(&versionsLeft))
	require.Zero(t, versionsLeft)

	for _, action := range []string{vault.AuditActionSecretCreate, vault.AuditActionSecretUpdate, vault.AuditActionSecretDelete} {
		entries := f.rec.byAction(action)
		require.NotEmpty(t, entries, action)
		e := entries[0]
		require.Equal(t, vault.AuditResourceSecret, e.ResourceKind)
		require.Equal(t, created.ID, e.ResourceID)
		require.Equal(t, "signing/api_key", e.ResourceName)
		require.Equal(t, f.tenant, e.TenantID)
		require.Equal(t, f.ns.ID, e.NamespaceID)
		require.Equal(t, string(authz.KindUser), e.ActorKind)
	}
	require.Len(t, f.rec.byAction(vault.AuditActionSecretUpdate), 3)

	// The path can be reused after deletion.
	recreated := f.create(t, "signing/api_key", "short")
	require.NotEqual(t, created.ID, recreated.ID)
	require.Equal(t, vault.SecretMask, recreated.MaskedValue)
}

func TestSecretCreateValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "dup", "value")
	tests := []struct {
		name   string
		in     vault.CreateSecretInput
		reason apperr.Reason
	}{
		{"invalid path", vault.CreateSecretInput{Path: "Bad", Value: "v"}, apperr.ReasonInvalidArgument},
		{"dot segment", vault.CreateSecretInput{Path: "a/../b", Value: "v"}, apperr.ReasonInvalidArgument},
		{"empty value", vault.CreateSecretInput{Path: "a", Value: ""}, apperr.ReasonInvalidArgument},
		{"value too large", vault.CreateSecretInput{Path: "a", Value: strings.Repeat("x", vault.MaxSecretValueBytes+1)}, apperr.ReasonInvalidArgument},
		{"invalid utf8", vault.CreateSecretInput{Path: "a", Value: "\xff\xfe"}, apperr.ReasonInvalidArgument},
		{"description too long", vault.CreateSecretInput{Path: "a", Value: "v", Description: strings.Repeat("d", vault.MaxSecretDescriptionLength+1)}, apperr.ReasonInvalidArgument},
		{"invalid description", vault.CreateSecretInput{Path: "a", Value: "v", Description: "\xff"}, apperr.ReasonInvalidArgument},
		{"too many tags", vault.CreateSecretInput{Path: "a", Value: "v", Tags: make([]string, vault.MaxSecretTags+1)}, apperr.ReasonInvalidArgument},
		{"empty tag", vault.CreateSecretInput{Path: "a", Value: "v", Tags: []string{""}}, apperr.ReasonInvalidArgument},
		{"control tag", vault.CreateSecretInput{Path: "a", Value: "v", Tags: []string{"a\nb"}}, apperr.ReasonInvalidArgument},
		{"long tag", vault.CreateSecretInput{Path: "a", Value: "v", Tags: []string{strings.Repeat("t", vault.MaxSecretTagLength+1)}}, apperr.ReasonInvalidArgument},
		{"duplicate path", vault.CreateSecretInput{Path: "dup", Value: "v"}, apperr.ReasonAlreadyExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.store.Create(ctx, f.admin(), f.ns, tt.in)
			requireReason(t, err, tt.reason)
		})
	}
	// The maximum value size is accepted.
	_, err := f.store.Create(ctx, f.admin(), f.ns, vault.CreateSecretInput{Path: "big", Value: strings.Repeat("x", vault.MaxSecretValueBytes)})
	require.NoError(t, err)

	_, err = f.store.Create(ctx, f.admin(), nil, vault.CreateSecretInput{Path: "a", Value: "v"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.store.Create(ctx, nil, f.ns, vault.CreateSecretInput{Path: "a", Value: "v"})
	requireReason(t, err, apperr.ReasonSessionInvalid)

	// The same path in another namespace is independent.
	_, err = f.store.Create(ctx, f.admin(), f.staging, vault.CreateSecretInput{Path: "dup", Value: "v"})
	require.NoError(t, err)

	// A namespace missing from the database violates the reference.
	ghost := *f.ns
	ghost.ID = "ns_missing"
	_, err = f.store.Create(ctx, platformAdmin(), &ghost, vault.CreateSecretInput{Path: "a", Value: "v"})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
}

func TestSecretUpdateValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "key", "value")
	tests := []struct {
		name   string
		id     string
		in     vault.UpdateSecretInput
		reason apperr.Reason
	}{
		{"expiry conflict", s.ID, vault.UpdateSecretInput{ExpiresAt: ptr(time.Now()), ClearExpiresAt: true}, apperr.ReasonInvalidArgument},
		{"empty value", s.ID, vault.UpdateSecretInput{Value: ptr("")}, apperr.ReasonInvalidArgument},
		{"bad description", s.ID, vault.UpdateSecretInput{Description: ptr(strings.Repeat("d", vault.MaxSecretDescriptionLength+1))}, apperr.ReasonInvalidArgument},
		{"bad tags", s.ID, vault.UpdateSecretInput{Tags: []string{""}, SetTags: true}, apperr.ReasonInvalidArgument},
		{"empty id", "", vault.UpdateSecretInput{Description: ptr("x")}, apperr.ReasonInvalidArgument},
		{"missing", "sec_missing", vault.UpdateSecretInput{Description: ptr("x")}, apperr.ReasonNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.store.Update(ctx, f.admin(), tt.id, tt.in)
			requireReason(t, err, tt.reason)
		})
	}
	_, err := f.store.Update(ctx, nil, s.ID, vault.UpdateSecretInput{})
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = f.store.Get(ctx, nil, s.ID)
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = f.store.Get(ctx, f.admin(), "")
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestSecretPermissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "signing/key", "value-0123456789")

	type op struct {
		name string
		run  func(p *authz.Principal) error
	}
	ops := []op{
		{"get", func(p *authz.Principal) error { _, err := f.store.Get(ctx, p, s.ID); return err }},
		{"list", func(p *authz.Principal) error {
			_, err := f.store.List(ctx, p, f.ns, vault.ListSecretsOptions{})
			return err
		}},
		{"versions", func(p *authz.Principal) error {
			_, err := f.store.ListVersions(ctx, p, s.ID, vault.ListVersionsOptions{})
			return err
		}},
		{"access logs", func(p *authz.Principal) error {
			_, err := f.store.ListAccessLogs(ctx, p, s.ID, vault.ListAccessLogsOptions{})
			return err
		}},
		{"create", func(p *authz.Principal) error {
			_, err := f.store.Create(ctx, p, f.ns, vault.CreateSecretInput{Path: "new/" + p.ID, Value: "v"})
			return err
		}},
		{"update", func(p *authz.Principal) error {
			_, err := f.store.Update(ctx, p, s.ID, vault.UpdateSecretInput{Description: ptr("d")})
			return err
		}},
		{"reveal", func(p *authz.Principal) error { _, err := f.store.Reveal(ctx, p, s.ID, 0); return err }},
	}
	ok := apperr.Reason("")
	denied := apperr.ReasonPermissionDenied
	notFound := apperr.ReasonNotFound
	scopeMissing := apperr.ReasonScopeMissing
	principals := []struct {
		name   string
		p      *authz.Principal
		expect map[string]apperr.Reason // op → reason; missing = ok
	}{
		{"platform admin", platformAdmin(), nil},
		{"owner", userWith(f.tenant, authz.RoleOwner, ""), nil},
		{"admin pinned to namespace", userWith(f.tenant, authz.RoleAdmin, f.ns.ID), nil},
		{"viewer", f.viewer(), map[string]apperr.Reason{"create": denied, "update": denied, "reveal": denied}},
		{"operator", f.operator(), map[string]apperr.Reason{"create": denied, "update": denied, "reveal": denied}},
		{"viewer with reveal", userWith(f.tenant, authz.RoleViewer, "", authz.PermSecretReveal), map[string]apperr.Reason{"create": denied, "update": denied}},
		{"admin of other namespace", userWith(f.tenant, authz.RoleAdmin, f.staging.ID), map[string]apperr.Reason{
			"get": notFound, "list": denied, "versions": notFound, "access logs": notFound, "create": denied, "update": notFound, "reveal": notFound,
		}},
		{"site restricted admin", f.siteRestricted(), map[string]apperr.Reason{
			"get": notFound, "list": denied, "versions": notFound, "access logs": notFound, "create": denied, "update": notFound, "reveal": notFound,
		}},
		{"other tenant owner", f.outsider(), map[string]apperr.Reason{
			"get": notFound, "list": denied, "versions": notFound, "access logs": notFound, "create": denied, "update": notFound, "reveal": notFound,
		}},
		{"admin token", tokenFor(t, f.ns, "admin"), nil},
		{"read-only token", tokenFor(t, f.ns, "secret:read:prod/*"), map[string]apperr.Reason{
			"get": notFound, "list": scopeMissing, "versions": notFound, "access logs": notFound, "create": scopeMissing, "update": notFound, "reveal": notFound,
		}},
		{"admin token of other namespace", tokenFor(t, f.staging, "admin"), map[string]apperr.Reason{
			"get": notFound, "list": scopeMissing, "versions": notFound, "access logs": notFound, "create": scopeMissing, "update": notFound, "reveal": notFound,
		}},
	}
	for _, pc := range principals {
		for _, o := range ops {
			t.Run(pc.name+"/"+o.name, func(t *testing.T) {
				want, has := pc.expect[o.name]
				if !has {
					want = ok
				}
				err := o.run(pc.p)
				if want == ok {
					require.NoError(t, err)
					return
				}
				requireReason(t, err, want)
			})
		}
	}

	// Delete last, as it removes the secret.
	for _, p := range []*authz.Principal{f.viewer(), f.operator()} {
		requireReason(t, f.store.Delete(ctx, p, s.ID), apperr.ReasonPermissionDenied)
	}
	requireReason(t, f.store.Delete(ctx, f.outsider(), s.ID), apperr.ReasonNotFound)
	requireReason(t, f.store.Delete(ctx, nil, s.ID), apperr.ReasonSessionInvalid)
	require.NoError(t, f.store.Delete(ctx, tokenFor(t, f.ns, "admin"), s.ID))

	denials := 0
	for _, e := range f.rec.byAction(vault.AuditActionSecretReveal) {
		if e.Result == audit.ResultDenied {
			denials++
		}
	}
	require.Equal(t, 2, denials, "viewer and operator reveal attempts are audited as denied")
}

func TestSecretList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.admin()
	inputs := []vault.CreateSecretInput{
		{Path: "signing/a_key", Value: "value-aaaa-1111", Tags: []string{"signing", "prod"}},
		{Path: "signing/b_key", Value: "value-bbbb-2222", Tags: []string{"signing"}, Description: "Signer TOKEN"},
		{Path: "signing_x/c", Value: "value-cccc-3333"},
		{Path: "vendor/key", Value: "short", Tags: []string{"prod"}},
		{Path: "zeta", Value: "value-zzzz-4444", Description: "last"},
	}
	for _, in := range inputs {
		_, err := f.store.Create(ctx, admin, f.ns, in)
		require.NoError(t, err)
	}
	_, err := f.store.Create(ctx, admin, f.staging, vault.CreateSecretInput{Path: "signing/a_key", Value: "other"})
	require.NoError(t, err)

	paths := func(page vault.SecretPage) []string {
		out := make([]string, len(page.Secrets))
		for i, s := range page.Secrets {
			out[i] = s.Path
		}
		return out
	}
	tests := []struct {
		name  string
		opts  vault.ListSecretsOptions
		want  []string
		total int64
	}{
		{"all", vault.ListSecretsOptions{}, []string{"signing/a_key", "signing/b_key", "signing_x/c", "vendor/key", "zeta"}, 5},
		{"prefix with underscore literal", vault.ListSecretsOptions{Prefix: "signing_"}, []string{"signing_x/c"}, 1},
		{"prefix dir", vault.ListSecretsOptions{Prefix: "signing/"}, []string{"signing/a_key", "signing/b_key"}, 2},
		{"search path", vault.ListSecretsOptions{Search: "KEY"}, []string{"signing/a_key", "signing/b_key", "vendor/key"}, 3},
		{"search description", vault.ListSecretsOptions{Search: "token"}, []string{"signing/b_key"}, 1},
		{"search percent is literal", vault.ListSecretsOptions{Search: "%"}, []string{}, 0},
		{"tags all of", vault.ListSecretsOptions{Tags: []string{"prod", "signing"}}, []string{"signing/a_key"}, 1},
		{"tag", vault.ListSecretsOptions{Tags: []string{"prod"}}, []string{"signing/a_key", "vendor/key"}, 2},
		{"combined", vault.ListSecretsOptions{Prefix: "signing", Tags: []string{"signing"}, Search: "b_"}, []string{"signing/b_key"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.store.List(ctx, f.viewer(), f.ns, tt.opts)
			require.NoError(t, err)
			require.Equal(t, tt.want, paths(page))
			require.Equal(t, tt.total, page.Total)
			require.False(t, page.HasMore)
		})
	}

	// Keyset pagination.
	var (
		all   []string
		after string
		pages int
	)
	for {
		page, err := f.store.List(ctx, f.viewer(), f.ns, vault.ListSecretsOptions{Limit: 2, AfterPath: after})
		require.NoError(t, err)
		require.EqualValues(t, 5, page.Total)
		all = append(all, paths(page)...)
		pages++
		if !page.HasMore {
			break
		}
		after = page.Secrets[len(page.Secrets)-1].Path
	}
	require.Equal(t, 3, pages)
	require.Equal(t, []string{"signing/a_key", "signing/b_key", "signing_x/c", "vendor/key", "zeta"}, all)

	// Masked values and metadata.
	page, err := f.store.List(ctx, f.viewer(), f.ns, vault.ListSecretsOptions{Prefix: "vendor"})
	require.NoError(t, err)
	require.Len(t, page.Secrets, 1)
	require.Equal(t, vault.SecretMask, page.Secrets[0].MaskedValue)
	page, err = f.store.List(ctx, f.viewer(), f.ns, vault.ListSecretsOptions{Prefix: "zeta"})
	require.NoError(t, err)
	require.Equal(t, vault.SecretMask+"4444", page.Secrets[0].MaskedValue)
	require.Equal(t, "prod", page.Secrets[0].NamespaceName)

	// A store whose cipher cannot open the values still lists them, masked.
	blind := vault.NewSecretStore(f.pool, vaulttest.NewCipher(t), nil, nil)
	page, err = blind.List(ctx, f.viewer(), f.ns, vault.ListSecretsOptions{Prefix: "zeta"})
	require.NoError(t, err)
	require.Equal(t, vault.SecretMask, page.Secrets[0].MaskedValue)

	invalid := []vault.ListSecretsOptions{
		{Prefix: strings.Repeat("p", vault.MaxSecretPathLength+1)},
		{AfterPath: "\xff"},
		{Search: strings.Repeat("s", vault.MaxSecretSearchLength+1)},
		{Tags: make([]string, 33)},
	}
	for _, opts := range invalid {
		_, err := f.store.List(ctx, f.viewer(), f.ns, opts)
		requireReason(t, err, apperr.ReasonInvalidArgument)
	}
	_, err = f.store.List(ctx, f.viewer(), nil, vault.ListSecretsOptions{})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.store.List(ctx, nil, f.ns, vault.ListSecretsOptions{})
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = f.store.ListVersions(ctx, f.viewer(), "sec_x", vault.ListVersionsOptions{BeforeVersion: -1})
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestSecretAccessLogs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "signing/key", "value-0123456789")
	_, err := f.store.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{Value: ptr("value-9876543210")})
	require.NoError(t, err)
	_, err = f.store.Reveal(ctx, f.admin(), s.ID, 1)
	require.NoError(t, err)
	node := tokenFor(t, f.ns, "secret:read:prod/signing/*")
	_, err = f.store.ReadSecret(ctx, node, f.ns, "signing/key", 0, "config")
	require.NoError(t, err)
	denied := tokenFor(t, f.ns, "secret:read:prod/other/*")
	_, err = f.store.ReadSecret(ctx, denied, f.ns, "signing/key", 0, "")
	requireReason(t, err, apperr.ReasonScopeMissing)

	// Another secret's entries must not leak into this log.
	f.create(t, "other/key", "x")

	var logs []vault.SecretAccessLog
	var cursor *vault.AccessLogCursor
	for {
		page, err := f.store.ListAccessLogs(ctx, f.viewer(), s.ID, vault.ListAccessLogsOptions{Limit: 2, After: cursor})
		require.NoError(t, err)
		logs = append(logs, page.Logs...)
		if !page.HasMore {
			break
		}
		last := page.Logs[len(page.Logs)-1]
		cursor = &vault.AccessLogCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	require.Len(t, logs, 5)
	actions := make([]string, len(logs))
	for i, l := range logs {
		actions[i] = l.Action
		if i > 0 {
			require.False(t, l.CreatedAt.After(logs[i-1].CreatedAt), "newest first")
		}
	}
	require.Equal(t, []string{
		vault.AuditActionSecretRead, vault.AuditActionSecretRead, vault.AuditActionSecretReveal,
		vault.AuditActionSecretUpdate, vault.AuditActionSecretCreate,
	}, actions)
	require.Equal(t, audit.ResultDenied, logs[0].Result)
	require.Equal(t, string(authz.KindToken), logs[0].ActorKind)
	require.Equal(t, denied.ID, logs[0].ActorID)
	require.Equal(t, "192.0.2.10", logs[0].IP)
	require.Equal(t, audit.ResultOK, logs[1].Result)
	require.Equal(t, 2, logs[1].Version)
	require.Equal(t, 1, logs[2].Version)
	require.Equal(t, 2, logs[3].Version)
	require.Equal(t, 1, logs[4].Version)

	_, err = f.store.ListAccessLogs(ctx, tokenFor(t, f.ns, "admin"), s.ID, vault.ListAccessLogsOptions{})
	require.NoError(t, err)
	_, err = f.store.ListAccessLogs(ctx, f.outsider(), s.ID, vault.ListAccessLogsOptions{})
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestSecretConcurrentUpdates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "rotating/key", "initial-value")
	const writers = 12

	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			_, err := f.store.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{
				Value: ptr("rotated-value-" + strings.Repeat("x", i)),
			})
			errs <- err
		}()
	}
	for range writers {
		require.NoError(t, <-errs)
	}
	got, err := f.store.Get(ctx, f.viewer(), s.ID)
	require.NoError(t, err)
	require.Equal(t, writers+1, got.CurrentVersion)
	versions, err := f.store.ListVersions(ctx, f.viewer(), s.ID, vault.ListVersionsOptions{})
	require.NoError(t, err)
	require.EqualValues(t, writers+1, versions.Total)
	for i, v := range versions.Versions {
		require.Equal(t, writers+1-i, v.Version, "versions are contiguous")
		revealed, err := f.store.Reveal(ctx, f.admin(), s.ID, v.Version)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(revealed.Value, "rotated-value-") || revealed.Value == "initial-value")
	}
}
