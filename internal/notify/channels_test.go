package notify

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

func TestChannelCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	user := &authz.Principal{Kind: authz.KindUser, ID: "usr_1", Name: "alice", TenantID: e.tenantID}

	created, err := e.svc.CreateChannel(ctx, user, ChannelInput{
		TenantID: e.tenantID, NamespaceID: e.ns.ID, Name: "ops", Kind: ChannelWebhook,
		Config: map[string]any{
			"url":     "https://hooks.example.com/services/abc/def12345",
			"secret":  "super-secret-value",
			"headers": map[string]any{"Authorization": "Bearer token-1234", "X-Team": "ops"},
		},
		EventTypes: []string{KindBreakerOpened, KindBanSpike}, SiteIDs: []string{e.site.ID}, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "ops", created.Name)
	require.Equal(t, e.ns.ID, created.NamespaceID)
	require.Equal(t, SeverityWarning, created.MinSeverity)
	require.Equal(t, []string{e.site.ID}, created.SiteIDs)
	require.Equal(t, "user:usr_1", created.CreatedBy)
	require.Equal(t, map[string]any{
		"url":     "https://hooks.example.com/••••2345",
		"secret":  "••••alue",
		"headers": map[string]any{"Authorization": "••••1234", "X-Team": "ops"},
	}, created.Config)

	// The settings are encrypted at rest.
	var ciphertext []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT config_ciphertext FROM notification_channels WHERE id = $1`, created.ID).Scan(&ciphertext))
	require.False(t, bytes.Contains(ciphertext, []byte("super-secret-value")))
	require.False(t, bytes.Contains(ciphertext, []byte("hooks.example.com")))

	got, err := e.svc.GetChannel(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Config, got.Config)

	// Duplicate names in the same tenant are rejected.
	_, err = e.svc.CreateChannel(ctx, user, ChannelInput{
		TenantID: e.tenantID, Name: "ops", Kind: ChannelWeCom,
		Config: map[string]any{"webhook_url": "https://qyapi.weixin.qq.com/x"}, EventTypes: []string{KindTest}, Enabled: true,
	})
	require.Equal(t, apperr.ReasonAlreadyExists, apperr.ReasonOf(err))

	// Update with the masked config sent back keeps secrets; other fields change.
	updated, err := e.svc.UpdateChannel(ctx, user, created.ID, ChannelUpdate{
		Name: "ops-renamed", Config: got.Config, EventTypes: []string{KindTest}, MinSeverity: SeverityCritical, Enabled: false,
	})
	require.NoError(t, err)
	require.Equal(t, "ops-renamed", updated.Name)
	require.False(t, updated.Enabled)
	require.Empty(t, updated.SiteIDs)
	require.Equal(t, created.Config, updated.Config)
	row, err := e.svc.q.NotifyChannelGet(ctx, created.ID)
	require.NoError(t, err)
	stored, err := e.svc.openConfig(row)
	require.NoError(t, err)
	require.Equal(t, "super-secret-value", stored.Secret)
	require.Equal(t, "Bearer token-1234", stored.Headers["Authorization"])
	require.Equal(t, "https://hooks.example.com/services/abc/def12345", stored.URL)

	// Update without config keeps everything.
	updated, err = e.svc.UpdateChannel(ctx, user, created.ID, ChannelUpdate{
		Name: "ops-renamed", EventTypes: []string{KindTest}, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, created.Config, updated.Config)

	// A changed secret replaces the stored one.
	updated, err = e.svc.UpdateChannel(ctx, user, created.ID, ChannelUpdate{
		Name: "ops-renamed", EventTypes: []string{KindTest}, Enabled: true,
		Config: map[string]any{"url": "https://hooks.example.com/new-endpoint-9999", "secret": "rotated-secret"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"url": "https://hooks.example.com/••••9999", "secret": "••••cret"}, updated.Config)

	// Tenant-wide channels cannot be restricted to sites.
	tenantWide, err := e.svc.CreateChannel(ctx, user, ChannelInput{
		TenantID: e.tenantID, Name: "tenant", Kind: ChannelTelegram,
		Config: map[string]any{"bot_token": "1:abc", "chat_id": 42.0}, EventTypes: []string{KindReportBacklog}, Enabled: true,
	})
	require.NoError(t, err)
	require.Empty(t, tenantWide.NamespaceID)
	_, err = e.svc.UpdateChannel(ctx, user, tenantWide.ID, ChannelUpdate{
		Name: "tenant", EventTypes: []string{KindTest}, SiteIDs: []string{e.site.ID},
	})
	require.ErrorContains(t, err, "namespace-bound")

	// Validation errors.
	for _, in := range []ChannelInput{
		{Name: "x", Kind: ChannelWebhook},
		{TenantID: e.tenantID, Name: "", Kind: ChannelWebhook},
		{TenantID: e.tenantID, Name: "x", Kind: "pager"},
		{TenantID: e.tenantID, Name: "x", Kind: ChannelWebhook, EventTypes: []string{KindTest}, Config: map[string]any{}},
		{TenantID: e.tenantID, Name: "x", Kind: ChannelWebhook, Config: map[string]any{"url": "https://x"}},
	} {
		_, err := e.svc.CreateChannel(ctx, user, in)
		require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err), "%+v", in)
	}
	_, err = e.svc.UpdateChannel(ctx, user, created.ID, ChannelUpdate{Name: " bad"})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = e.svc.UpdateChannel(ctx, user, "nch_missing", ChannelUpdate{Name: "x", EventTypes: []string{KindTest}})
	require.True(t, apperr.IsNotFound(err))
	_, err = e.svc.UpdateChannel(ctx, user, created.ID, ChannelUpdate{Name: "x", EventTypes: []string{KindTest},
		Config: map[string]any{"url": "ftp://nope"}})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	// A missing namespace violates the foreign key.
	_, err = e.svc.CreateChannel(ctx, user, ChannelInput{
		TenantID: e.tenantID, NamespaceID: "ns_missing", Name: "orphan", Kind: ChannelWeCom,
		Config: map[string]any{"webhook_url": "https://x"}, EventTypes: []string{KindTest},
	})
	require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(err))

	// Listing with visibility and pagination.
	page, err := e.svc.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID, IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.True(t, page.More)
	require.Equal(t, "ops-renamed", page.Channels[0].Name)
	page, err = e.svc.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID, IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, AfterName: "ops-renamed"})
	require.NoError(t, err)
	require.False(t, page.More)
	require.Len(t, page.Channels, 1)
	require.Equal(t, "tenant", page.Channels[0].Name)
	page, err = e.svc.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID, NamespaceIDs: []string{e.ns.ID}})
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	page, err = e.svc.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID})
	require.NoError(t, err)
	require.Empty(t, page.Channels)
	_, err = e.svc.ListChannels(ctx, ChannelQuery{})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))

	// Delete.
	require.NoError(t, e.svc.DeleteChannel(ctx, user, created.ID))
	_, err = e.svc.GetChannel(ctx, created.ID)
	require.True(t, apperr.IsNotFound(err))
	require.True(t, apperr.IsNotFound(e.svc.DeleteChannel(ctx, user, created.ID)))

	require.Equal(t, []string{
		AuditChannelCreate, AuditChannelUpdate, AuditChannelUpdate, AuditChannelUpdate, AuditChannelCreate, AuditChannelDelete,
	}, e.audit.actions())
	for _, entry := range e.audit.entries {
		require.NotContains(t, entry.Details, "config")
		require.Equal(t, "usr_1", entry.ActorID)
	}
}

func TestChannelUndecryptableConfig(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	ch := e.webhookChannel("hook", e.ns.ID, "https://hooks.example.com/abc", []string{KindTest}, nil, "")

	// A service with a different KEK cannot decrypt the settings but can still
	// show, update (with full settings) and delete the channel.
	other := New(Config{}, e.pool, vaulttest.NewCipher(t), e.rdb, e.keys, e.cat, nil, nil, nil, nil)
	got, err := other.GetChannel(ctx, ch.ID)
	require.NoError(t, err)
	require.Empty(t, got.Config)
	page, err := other.ListChannels(ctx, ChannelQuery{TenantID: e.tenantID, NamespaceIDs: []string{e.ns.ID}})
	require.NoError(t, err)
	require.Empty(t, page.Channels[0].Config)
	updated, err := other.UpdateChannel(ctx, admin, ch.ID, ChannelUpdate{Name: "hook2", EventTypes: []string{KindTest}, Enabled: true})
	require.NoError(t, err)
	require.Empty(t, updated.Config)
	updated, err = other.UpdateChannel(ctx, admin, ch.ID, ChannelUpdate{Name: "hook2", EventTypes: []string{KindTest}, Enabled: true,
		Config: map[string]any{"url": "https://hooks.example.com/new"}})
	require.NoError(t, err)
	require.Equal(t, "https://hooks.example.com/••••", updated.Config["url"])

	d, err := e.svc.TestChannel(ctx, admin, ch.ID)
	require.NoError(t, err)
	require.False(t, d.OK)
	require.Equal(t, "channel settings cannot be decrypted", d.Error)
	require.NoError(t, other.DeleteChannel(ctx, admin, ch.ID))
}
