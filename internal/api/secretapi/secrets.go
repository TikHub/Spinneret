package secretapi

import (
	"context"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Page cursors (opaque to clients, see apiutil.EncodeCursor).
type (
	secretCursor struct {
		After string `json:"a"`
	}
	versionCursor struct {
		Before int `json:"b"`
	}
	accessLogCursor struct {
		CreatedAt time.Time `json:"t"`
		ID        string    `json:"i"`
	}
)

// ListSecrets lists secret metadata of a namespace (secret:list).
func (h *Handler) ListSecrets(ctx context.Context, req *connect.Request[spinneretv1.ListSecretsRequest]) (*connect.Response[spinneretv1.ListSecretsResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	var cur secretCursor
	if _, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	page, err := h.secrets.List(ctx, p, ns, vault.ListSecretsOptions{
		Search:    req.Msg.GetSearch(),
		Tags:      req.Msg.GetTags(),
		Limit:     apiutil.PageSize(req.Msg.GetPageSize()),
		AfterPath: cur.After,
	})
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListSecretsResponse{
		Secrets: make([]*spinneretv1.SecretInfo, 0, len(page.Secrets)),
		Total:   clampInt32(page.Total),
	}
	for _, s := range page.Secrets {
		resp.Secrets = append(resp.Secrets, secretInfo(s))
	}
	if page.HasMore && len(page.Secrets) > 0 {
		if resp.NextPageToken, err = encodeCursor(secretCursor{After: page.Secrets[len(page.Secrets)-1].Path}); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

// GetSecret returns the metadata of one secret (secret:list).
func (h *Handler) GetSecret(ctx context.Context, req *connect.Request[spinneretv1.SecretAdminServiceGetSecretRequest]) (*connect.Response[spinneretv1.SecretAdminServiceGetSecretResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.secrets.Get(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.SecretAdminServiceGetSecretResponse{Secret: secretInfo(s)}), nil
}

// CreateSecret creates a secret with its first version (secret:write).
func (h *Handler) CreateSecret(ctx context.Context, req *connect.Request[spinneretv1.CreateSecretRequest]) (*connect.Response[spinneretv1.CreateSecretResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	expiresAt, err := optionalTime("expires_at", req.Msg.GetExpiresAt())
	if err != nil {
		return nil, err
	}
	s, err := h.secrets.Create(ctx, p, ns, vault.CreateSecretInput{
		Path:        req.Msg.GetPath(),
		Value:       req.Msg.GetValue(),
		Description: req.Msg.GetDescription(),
		Tags:        req.Msg.GetTags(),
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateSecretResponse{Secret: secretInfo(s)}), nil
}

// UpdateSecret updates metadata and optionally stores a new version (secret:write).
func (h *Handler) UpdateSecret(ctx context.Context, req *connect.Request[spinneretv1.UpdateSecretRequest]) (*connect.Response[spinneretv1.UpdateSecretResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	expiresAt, err := optionalTime("expires_at", req.Msg.GetExpiresAt())
	if err != nil {
		return nil, err
	}
	s, err := h.secrets.Update(ctx, p, req.Msg.GetId(), vault.UpdateSecretInput{
		Value:          req.Msg.Value,
		Description:    req.Msg.Description,
		Tags:           req.Msg.GetTags(),
		SetTags:        req.Msg.GetSetTags(),
		ExpiresAt:      expiresAt,
		ClearExpiresAt: req.Msg.GetClearExpiresAt(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateSecretResponse{Secret: secretInfo(s)}), nil
}

// DeleteSecret deletes a secret and all of its versions (secret:write).
func (h *Handler) DeleteSecret(ctx context.Context, req *connect.Request[spinneretv1.DeleteSecretRequest]) (*connect.Response[spinneretv1.DeleteSecretResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.secrets.Delete(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteSecretResponse{}), nil
}

// RevealSecret returns the plaintext of a secret version (secret:reveal, audited).
func (h *Handler) RevealSecret(ctx context.Context, req *connect.Request[spinneretv1.RevealSecretRequest]) (*connect.Response[spinneretv1.RevealSecretResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.secrets.Reveal(ctx, p, req.Msg.GetId(), int(req.Msg.GetVersion()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RevealSecretResponse{Value: v.Value, Version: int32(v.Version)}), nil
}

// ListSecretVersions lists version metadata, newest first (secret:list).
func (h *Handler) ListSecretVersions(ctx context.Context, req *connect.Request[spinneretv1.ListSecretVersionsRequest]) (*connect.Response[spinneretv1.ListSecretVersionsResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var cur versionCursor
	if _, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	if cur.Before < 0 || cur.Before > math.MaxInt32 {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid page_token")
	}
	page, err := h.secrets.ListVersions(ctx, p, req.Msg.GetId(), vault.ListVersionsOptions{
		Limit: apiutil.PageSize(req.Msg.GetPageSize()), BeforeVersion: cur.Before,
	})
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListSecretVersionsResponse{
		Versions: make([]*spinneretv1.SecretVersion, 0, len(page.Versions)),
		Total:    clampInt32(page.Total),
	}
	for _, v := range page.Versions {
		resp.Versions = append(resp.Versions, &spinneretv1.SecretVersion{
			Version:   int32(v.Version),
			CreatedBy: v.CreatedBy,
			CreatedAt: apiutil.Timestamp(v.CreatedAt),
			KekId:     v.KEKID,
		})
	}
	if page.HasMore && len(page.Versions) > 0 {
		if resp.NextPageToken, err = encodeCursor(versionCursor{Before: page.Versions[len(page.Versions)-1].Version}); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

// ListSecretAccessLogs lists audited reads and changes of a secret, newest
// first (secret:list or audit:read).
func (h *Handler) ListSecretAccessLogs(ctx context.Context, req *connect.Request[spinneretv1.ListSecretAccessLogsRequest]) (*connect.Response[spinneretv1.ListSecretAccessLogsResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var cur accessLogCursor
	hasCursor, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur)
	if err != nil {
		return nil, err
	}
	opts := vault.ListAccessLogsOptions{Limit: apiutil.PageSize(req.Msg.GetPageSize())}
	if hasCursor {
		if cur.ID == "" || cur.CreatedAt.IsZero() {
			return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid page_token")
		}
		opts.After = &vault.AccessLogCursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	page, err := h.secrets.ListAccessLogs(ctx, p, req.Msg.GetId(), opts)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListSecretAccessLogsResponse{Logs: make([]*spinneretv1.SecretAccessLog, 0, len(page.Logs))}
	for _, l := range page.Logs {
		resp.Logs = append(resp.Logs, &spinneretv1.SecretAccessLog{
			CreatedAt: apiutil.Timestamp(l.CreatedAt),
			ActorKind: l.ActorKind,
			ActorId:   l.ActorID,
			ActorName: l.ActorName,
			Action:    l.Action,
			Result:    l.Result,
			Ip:        l.IP,
			Version:   int32(min(l.Version, math.MaxInt32)),
		})
	}
	if page.HasMore && len(page.Logs) > 0 {
		last := page.Logs[len(page.Logs)-1]
		if resp.NextPageToken, err = encodeCursor(accessLogCursor{CreatedAt: last.CreatedAt, ID: last.ID}); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

func secretInfo(s vault.Secret) *spinneretv1.SecretInfo {
	return &spinneretv1.SecretInfo{
		Id:             s.ID,
		Namespace:      s.NamespaceName,
		Path:           s.Path,
		Description:    s.Description,
		Tags:           s.Tags,
		CurrentVersion: int32(min(s.CurrentVersion, math.MaxInt32)),
		MaskedValue:    s.MaskedValue,
		ExpiresAt:      apiutil.TimestampPtr(s.ExpiresAt),
		LastAccessedAt: apiutil.TimestampPtr(s.LastAccessedAt),
		CreatedBy:      s.CreatedBy,
		CreatedAt:      apiutil.Timestamp(s.CreatedAt),
		UpdatedAt:      apiutil.Timestamp(s.UpdatedAt),
	}
}

// optionalTime converts an optional timestamp, rejecting invalid values.
func optionalTime(field string, ts *timestamppb.Timestamp) (*time.Time, error) {
	if ts == nil {
		return nil, nil
	}
	if err := ts.CheckValid(); err != nil {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "%s is not a valid timestamp", field)
	}
	t := ts.AsTime()
	return &t, nil
}

func encodeCursor(v any) (string, error) {
	token, err := apiutil.EncodeCursor(v)
	if err != nil {
		return "", apperr.Internal(err)
	}
	return token, nil
}

func clampInt32(n int64) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < 0:
		return 0
	default:
		return int32(n)
	}
}
