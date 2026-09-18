package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/notify/notifydb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// Audit actions and resource kind of channel mutations.
const (
	AuditChannelCreate = "notify.channel.create"
	AuditChannelUpdate = "notify.channel.update"
	AuditChannelDelete = "notify.channel.delete"
	AuditChannelTest   = "notify.channel.test"
	AuditResourceKind  = "notification_channel"
	configAADField     = "config"
	maxListLimit       = 500
)

// CreateChannel validates and stores a new channel with sealed settings.
func (s *Service) CreateChannel(ctx context.Context, p *authz.Principal, in ChannelInput) (Channel, error) {
	if in.TenantID == "" {
		return Channel{}, apperr.InvalidArgument("", "tenant is required")
	}
	if err := validateChannelName(in.Name); err != nil {
		return Channel{}, err
	}
	if !ValidChannelKind(in.Kind) {
		return Channel{}, apperr.InvalidArgument("", "unsupported channel kind %q", truncateBytes(in.Kind, 32))
	}
	eventTypes, siteIDs, minSeverity, err := normalizeFilters(in.NamespaceID, in.EventTypes, in.SiteIDs, in.MinSeverity)
	if err != nil {
		return Channel{}, err
	}
	cfg, err := parseConfig(in.Kind, in.Config)
	if err != nil {
		return Channel{}, err
	}
	id := idgen.New(idgen.Channel)
	sealed, err := s.sealConfig(id, cfg)
	if err != nil {
		return Channel{}, err
	}
	row, err := s.q.NotifyChannelInsert(ctx, notifydb.NotifyChannelInsertParams{
		ID:               id,
		TenantID:         in.TenantID,
		NamespaceID:      optionalString(in.NamespaceID),
		Name:             in.Name,
		Kind:             in.Kind,
		ConfigCiphertext: sealed.Ciphertext,
		ConfigWrappedDek: sealed.WrappedDEK,
		ConfigKekID:      sealed.KEKID,
		EventTypes:       eventTypes,
		SiteIds:          siteIDs,
		MinSeverity:      minSeverity,
		Enabled:          in.Enabled,
		CreatedBy:        p.Actor(),
	})
	if err != nil {
		return Channel{}, mapChannelError(err, in.Name)
	}
	s.channels.invalidate(in.TenantID)
	s.recordAudit(ctx, p, AuditChannelCreate, row, map[string]any{
		"kind": row.Kind, "event_types": row.EventTypes, "site_ids": row.SiteIds,
		"min_severity": row.MinSeverity, "enabled": row.Enabled,
	})
	return channelView(row, cfg), nil
}

// UpdateChannel replaces the mutable fields of a channel. Config nil keeps the
// stored settings; masked values sent back unchanged keep stored secrets.
func (s *Service) UpdateChannel(ctx context.Context, p *authz.Principal, id string, upd ChannelUpdate) (Channel, error) {
	if err := validateChannelName(upd.Name); err != nil {
		return Channel{}, err
	}
	var (
		row       notifydb.NotificationChannel
		cfg       channelConfig
		cfgChange bool
	)
	err := s.inTx(ctx, func(q *notifydb.Queries) error {
		cur, err := q.NotifyChannelGetForUpdate(ctx, id)
		if err != nil {
			return mapChannelError(err, id)
		}
		eventTypes, siteIDs, minSeverity, err := normalizeFilters(deref(cur.NamespaceID), upd.EventTypes, upd.SiteIDs, upd.MinSeverity)
		if err != nil {
			return err
		}
		sealed := vault.Sealed{Ciphertext: cur.ConfigCiphertext, WrappedDEK: cur.ConfigWrappedDek, KEKID: cur.ConfigKekID}
		if upd.Config != nil {
			// Merging masked values needs the stored settings; when they cannot
			// be decrypted every secret must be sent again in full.
			stored, openErr := s.openConfig(cur)
			if openErr != nil {
				s.logger.Error("open channel config failed", "channel_id", cur.ID, "error", openErr)
			}
			if cfg, err = parseConfig(cur.Kind, mergeMasked(cur.Kind, stored, upd.Config)); err != nil {
				return err
			}
			if sealed, err = s.sealConfig(cur.ID, cfg); err != nil {
				return err
			}
			cfgChange = true
		}
		row, err = q.NotifyChannelUpdate(ctx, notifydb.NotifyChannelUpdateParams{
			ID:               cur.ID,
			Name:             upd.Name,
			ConfigCiphertext: sealed.Ciphertext,
			ConfigWrappedDek: sealed.WrappedDEK,
			ConfigKekID:      sealed.KEKID,
			EventTypes:       eventTypes,
			SiteIds:          siteIDs,
			MinSeverity:      minSeverity,
			Enabled:          upd.Enabled,
		})
		return mapChannelError(err, upd.Name)
	})
	if err != nil {
		return Channel{}, err
	}
	s.channels.invalidate(row.TenantID)
	s.recordAudit(ctx, p, AuditChannelUpdate, row, map[string]any{
		"config_changed": cfgChange, "event_types": row.EventTypes, "site_ids": row.SiteIds,
		"min_severity": row.MinSeverity, "enabled": row.Enabled,
	})
	if cfgChange {
		return channelView(row, cfg), nil
	}
	return s.view(row), nil
}

// DeleteChannel deletes a channel.
func (s *Service) DeleteChannel(ctx context.Context, p *authz.Principal, id string) error {
	row, err := s.q.NotifyChannelDelete(ctx, id)
	if err != nil {
		return mapChannelError(err, id)
	}
	s.channels.invalidate(row.TenantID)
	s.recordAudit(ctx, p, AuditChannelDelete, row, map[string]any{"kind": row.Kind})
	return nil
}

// GetChannel returns a channel with masked settings.
func (s *Service) GetChannel(ctx context.Context, id string) (Channel, error) {
	row, err := s.q.NotifyChannelGet(ctx, id)
	if err != nil {
		return Channel{}, mapChannelError(err, id)
	}
	return s.view(row), nil
}

// ListChannels returns a page of channels ordered by name.
func (s *Service) ListChannels(ctx context.Context, query ChannelQuery) (ChannelPage, error) {
	if query.TenantID == "" {
		return ChannelPage{}, apperr.InvalidArgument("", "tenant is required")
	}
	nsIDs := nonNil(query.NamespaceIDs)
	if !query.IncludeTenant && len(nsIDs) == 0 {
		return ChannelPage{Channels: []Channel{}}, nil
	}
	limit := clampLimit(query.Limit)
	rows, err := s.q.NotifyChannelList(ctx, notifydb.NotifyChannelListParams{
		TenantID:      query.TenantID,
		IncludeTenant: query.IncludeTenant,
		NamespaceIds:  nsIDs,
		AfterName:     query.AfterName,
		LimitRows:     int32(limit + 1),
	})
	if err != nil {
		return ChannelPage{}, fmt.Errorf("list channels: %w", err)
	}
	total, err := s.q.NotifyChannelCount(ctx, notifydb.NotifyChannelCountParams{
		TenantID: query.TenantID, IncludeTenant: query.IncludeTenant, NamespaceIds: nsIDs,
	})
	if err != nil {
		return ChannelPage{}, fmt.Errorf("count channels: %w", err)
	}
	page := ChannelPage{Total: int(total), More: len(rows) > limit}
	if page.More {
		rows = rows[:limit]
	}
	page.Channels = make([]Channel, 0, len(rows))
	for _, row := range rows {
		page.Channels = append(page.Channels, s.view(row))
	}
	return page, nil
}

// view returns the masked view of a channel row. A channel whose settings
// cannot be decrypted (for example after its KEK was removed) is shown with
// empty settings so it can still be fixed or deleted.
func (s *Service) view(row notifydb.NotificationChannel) Channel {
	cfg, err := s.openConfig(row)
	if err != nil {
		s.logger.Error("open channel config failed", "channel_id", row.ID, "error", err)
		v := channelView(row, channelConfig{})
		v.Config = map[string]any{}
		return v
	}
	return channelView(row, cfg)
}

// sealConfig encrypts a channel config bound to the channel ID.
func (s *Service) sealConfig(channelID string, cfg channelConfig) (vault.Sealed, error) {
	plain, err := cfg.encode()
	if err != nil {
		return vault.Sealed{}, apperr.Internal(err)
	}
	defer clear(plain)
	sealed, err := s.cipher.Seal(plain, vault.AAD(channelID, configAADField))
	if err != nil {
		return vault.Sealed{}, apperr.Internal(fmt.Errorf("seal channel config: %w", err))
	}
	return sealed, nil
}

// openConfig decrypts the config of a channel row.
func (s *Service) openConfig(row notifydb.NotificationChannel) (channelConfig, error) {
	plain, err := s.cipher.Open(vault.Sealed{
		Ciphertext: row.ConfigCiphertext, WrappedDEK: row.ConfigWrappedDek, KEKID: row.ConfigKekID,
	}, vault.AAD(row.ID, configAADField))
	if err != nil {
		return channelConfig{}, err
	}
	defer clear(plain)
	return decodeConfig(plain)
}

func (s *Service) inTx(ctx context.Context, fn func(q *notifydb.Queries) error) error {
	return pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		return fn(s.q.WithTx(tx))
	})
}

func (s *Service) recordAudit(ctx context.Context, p *authz.Principal, action string, row notifydb.NotificationChannel, details map[string]any) {
	s.audit.Record(ctx, audit.FromPrincipal(p, row.TenantID, deref(row.NamespaceID), action,
		AuditResourceKind, row.ID, row.Name, audit.ResultOK, details))
}

// channelView converts a row and its decrypted config into the masked view.
func channelView(row notifydb.NotificationChannel, cfg channelConfig) Channel {
	return Channel{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		NamespaceID:        deref(row.NamespaceID),
		Name:               row.Name,
		Kind:               row.Kind,
		Config:             cfg.masked(row.Kind),
		EventTypes:         nonNil(row.EventTypes),
		SiteIDs:            nonNil(row.SiteIds),
		MinSeverity:        row.MinSeverity,
		Enabled:            row.Enabled,
		LastDeliveryAt:     row.LastDeliveryAt,
		LastDeliveryStatus: row.LastDeliveryStatus,
		CreatedBy:          row.CreatedBy,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func validateChannelName(name string) error {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > MaxChannelNameLen || strings.TrimSpace(name) != name || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "\r\n\t\x00") {
		return apperr.InvalidArgument("", "name must be 1..%d characters without surrounding spaces or control characters", MaxChannelNameLen)
	}
	return nil
}

// normalizeFilters validates the subscription filters of a channel.
func normalizeFilters(namespaceID string, eventTypes, siteIDs []string, minSeverity string) ([]string, []string, string, error) {
	kinds, err := normalizeEventTypes(eventTypes)
	if err != nil {
		return nil, nil, "", err
	}
	if namespaceID == "" && len(siteIDs) > 0 {
		return nil, nil, "", apperr.InvalidArgument("", "sites require a namespace-bound channel")
	}
	sites, err := normalizeSiteIDs(siteIDs)
	if err != nil {
		return nil, nil, "", err
	}
	if minSeverity == "" {
		minSeverity = SeverityWarning
	}
	if !ValidSeverity(minSeverity) {
		return nil, nil, "", apperr.InvalidArgument("", "min_severity must be info, warning or critical")
	}
	return kinds, sites, minSeverity, nil
}

func mapChannelError(err error, what string) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("notification channel not found").WithCause(err)
	}
	return pgstore.MapError(err, fmt.Sprintf("notification channel %q", truncateBytes(what, 64)))
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > maxListLimit:
		return maxListLimit
	default:
		return n
	}
}
