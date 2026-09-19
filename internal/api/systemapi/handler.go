// Package systemapi implements the Connect SystemService: facts about the
// deployment itself rather than about the data inside it.
package systemapi

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/settings"
	"github.com/TikHub/Spinneret/internal/updatecheck"
)

// Handler implements spinneretv1connect.SystemServiceHandler.
type Handler struct {
	updates  *updatecheck.Checker
	settings *settings.Store
	audit    audit.Recorder
	logger   *slog.Logger
}

var _ spinneretv1connect.SystemServiceHandler = (*Handler)(nil)

// New creates the SystemService handler. A nil checker answers "disabled".
func New(updates *updatecheck.Checker, store *settings.Store, rec audit.Recorder, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if rec == nil {
		rec = audit.Nop{}
	}
	return &Handler{
		updates:  updates,
		settings: store,
		audit:    rec,
		logger:   logger.With(slog.String("component", "systemapi")),
	}
}

// user returns the console principal, rejecting node tokens: everything on this
// service is an operator action against the deployment rather than something a
// node has any business doing.
func (h *Handler) user(ctx context.Context) (*authz.Principal, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Kind != authz.KindUser {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "a console user session is required")
	}
	return p, nil
}

// ListSettings returns the deployment settings and where each value came from.
func (h *Handler) ListSettings(ctx context.Context, _ *connect.Request[spinneretv1.ListSettingsRequest]) (*connect.Response[spinneretv1.ListSettingsResponse], error) {
	p, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.settings.List(ctx)
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("read deployment settings: %w", err))
	}
	return connect.NewResponse(&spinneretv1.ListSettingsResponse{
		Settings: protoSettings(list),
		CanEdit:  p.IsPlatformAdmin,
	}), nil
}

// UpdateSettings changes deployment settings.
func (h *Handler) UpdateSettings(ctx context.Context, req *connect.Request[spinneretv1.UpdateSettingsRequest]) (*connect.Response[spinneretv1.UpdateSettingsResponse], error) {
	p, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	// Deployment-wide and destructive: shortening a retention drops partitions on
	// the next hourly pass. A tenant owner administers a tenant, not the disk.
	if !p.IsPlatformAdmin {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "changing deployment settings requires a platform administrator")
	}
	values := req.Msg.GetValues()
	if len(values) == 0 {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "no settings given")
	}

	list, err := h.settings.Update(ctx, values)
	if err != nil {
		h.record(ctx, p, values, audit.ResultDenied)
		// Every failure out of Update is the caller's: a pinned setting, an unknown
		// key, or a value outside its bounds. Each message already names the key
		// and what was wrong with it, which is what the console shows.
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "%s", err.Error())
	}
	h.record(ctx, p, values, audit.ResultOK)
	return connect.NewResponse(&spinneretv1.UpdateSettingsResponse{Settings: protoSettings(list)}), nil
}

// record audits the change. Retention decides how long evidence is kept, so the
// change to it is itself evidence.
func (h *Handler) record(ctx context.Context, p *authz.Principal, values map[string]string, result string) {
	details := make(map[string]any, len(values))
	for k, v := range values {
		if v == "" {
			details[k] = "(reset to default)"
			continue
		}
		details[k] = v
	}
	h.audit.Record(ctx, audit.FromPrincipal(p, "", "", "settings.update", "deployment_settings", "retention", "retention", result, details))
}

func protoSettings(in []settings.Setting) []*spinneretv1.Setting {
	out := make([]*spinneretv1.Setting, 0, len(in))
	for _, s := range in {
		out = append(out, &spinneretv1.Setting{
			Key:          s.Key,
			Value:        s.Value,
			DefaultValue: s.Default,
			Origin:       protoOrigin(s.Origin),
			EnvVar:       s.EnvVar,
			Unit:         string(s.Unit),
			Minimum:      s.Min,
			Maximum:      s.Max,
		})
	}
	return out
}

func protoOrigin(o settings.Origin) spinneretv1.SettingOrigin {
	switch o {
	case settings.OriginDatabase:
		return spinneretv1.SettingOrigin_SETTING_ORIGIN_DATABASE
	case settings.OriginEnvironment:
		return spinneretv1.SettingOrigin_SETTING_ORIGIN_ENVIRONMENT
	case settings.OriginDefault:
		return spinneretv1.SettingOrigin_SETTING_ORIGIN_DEFAULT
	}
	return spinneretv1.SettingOrigin_SETTING_ORIGIN_UNSPECIFIED
}

// CheckForUpdate compares the running build against the latest release.
//
// A feed that cannot be reached is not an RPC error: the console asked two
// questions at once — "what am I running" and "is there something newer" — and
// the first still has an answer. The failure is reported in the response so the
// version stays visible.
func (h *Handler) CheckForUpdate(ctx context.Context, _ *connect.Request[spinneretv1.CheckForUpdateRequest]) (*connect.Response[spinneretv1.CheckForUpdateResponse], error) {
	// Reaching out to the network is an operator action, so it is not something
	// a node token may trigger.
	if _, err := h.user(ctx); err != nil {
		return nil, err
	}

	res, checkErr := h.updates.Check(ctx)
	out := &spinneretv1.CheckForUpdateResponse{
		CurrentVersion:   res.Current,
		LatestVersion:    res.Latest,
		ReleaseUrl:       res.ReleaseURL,
		UpdateAvailable:  res.UpdateAvailable,
		CurrentIsRelease: res.CurrentIsRelease,
		Disabled:         res.Disabled,
	}
	if !res.CheckedAt.IsZero() {
		out.CheckedAt = timestamppb.New(res.CheckedAt)
	}
	if checkErr != nil {
		out.Error = checkErr.Error()
		h.logger.Warn("update check failed", slog.Any("error", checkErr))
	}
	return connect.NewResponse(out), nil
}
