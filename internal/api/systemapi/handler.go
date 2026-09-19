// Package systemapi implements the Connect SystemService: facts about the
// deployment itself rather than about the data inside it.
package systemapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/updatecheck"
)

// Handler implements spinneretv1connect.SystemServiceHandler.
type Handler struct {
	updates *updatecheck.Checker
	logger  *slog.Logger
}

var _ spinneretv1connect.SystemServiceHandler = (*Handler)(nil)

// New creates the SystemService handler. A nil checker answers "disabled".
func New(updates *updatecheck.Checker, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{updates: updates, logger: logger.With(slog.String("component", "systemapi"))}
}

// CheckForUpdate compares the running build against the latest release.
//
// A feed that cannot be reached is not an RPC error: the console asked two
// questions at once — "what am I running" and "is there something newer" — and
// the first still has an answer. The failure is reported in the response so the
// version stays visible.
func (h *Handler) CheckForUpdate(ctx context.Context, _ *connect.Request[spinneretv1.CheckForUpdateRequest]) (*connect.Response[spinneretv1.CheckForUpdateResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	// Reaching out to the network is an operator action, so it is not something
	// a node token may trigger.
	if p.Kind != authz.KindUser {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "a console user session is required")
	}

	res, checkErr := h.updates.Check(ctx)
	out := &spinneretv1.CheckForUpdateResponse{
		CurrentVersion:  res.Current,
		LatestVersion:   res.Latest,
		ReleaseUrl:      res.ReleaseURL,
		UpdateAvailable: res.UpdateAvailable,
		Disabled:        res.Disabled,
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
