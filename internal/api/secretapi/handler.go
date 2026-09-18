// Package secretapi implements the Connect handlers of the vault:
// SecretService (node reads of secrets) and SecretAdminService (secret
// management, audited reveals and KEK rewrap).
package secretapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/vault"
)

// nodeReadPurpose is the purpose recorded for SecretService reads.
const nodeReadPurpose = "api"

// SecretManager is the secret management API used by Handler (provided by
// *vault.SecretStore). Implementations enforce permissions and audit.
type SecretManager interface {
	Create(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in vault.CreateSecretInput) (vault.Secret, error)
	Update(ctx context.Context, p *authz.Principal, id string, in vault.UpdateSecretInput) (vault.Secret, error)
	Delete(ctx context.Context, p *authz.Principal, id string) error
	Get(ctx context.Context, p *authz.Principal, id string) (vault.Secret, error)
	List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, opts vault.ListSecretsOptions) (vault.SecretPage, error)
	Reveal(ctx context.Context, p *authz.Principal, id string, version int) (vault.SecretValue, error)
	ListVersions(ctx context.Context, p *authz.Principal, id string, opts vault.ListVersionsOptions) (vault.SecretVersionPage, error)
	ListAccessLogs(ctx context.Context, p *authz.Principal, id string, opts vault.ListAccessLogsOptions) (vault.SecretAccessLogPage, error)
}

// SecretReader reads secrets on behalf of a principal with authorization and
// audit (provided by *vault.SecretStore).
type SecretReader interface {
	ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (vault.SecretValue, error)
}

// KEKManager starts and reports KEK rewrap jobs (provided by *vault.Rewrapper).
type KEKManager interface {
	Start(ctx context.Context) (bool, error)
	Status(ctx context.Context) (vault.KEKStatus, error)
}

// Handler implements spinneretv1connect.SecretAdminServiceHandler.
type Handler struct {
	secrets SecretManager
	kek     KEKManager
	cat     catalog.Catalog
	audit   audit.Recorder
	logger  *slog.Logger
}

var _ spinneretv1connect.SecretAdminServiceHandler = (*Handler)(nil)

// New returns the SecretAdminService handler. rec receives the audit entry of
// StartKEKRewrap (secret mutations are audited by secrets itself).
func New(secrets SecretManager, kek KEKManager, cat catalog.Catalog, rec audit.Recorder, logger *slog.Logger) *Handler {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{secrets: secrets, kek: kek, cat: cat, audit: rec, logger: logger}
}

// NodeHandler implements spinneretv1connect.SecretServiceHandler. SecretService
// and SecretAdminService both declare a GetSecret method with different
// signatures, so they are served by two handler types.
type NodeHandler struct {
	secrets SecretReader
	cat     catalog.Catalog
}

var _ spinneretv1connect.SecretServiceHandler = (*NodeHandler)(nil)

// NewNodeHandler returns the SecretService handler.
func NewNodeHandler(secrets SecretReader, cat catalog.Catalog) *NodeHandler {
	return &NodeHandler{secrets: secrets, cat: cat}
}

// GetSecret returns a secret version of the token namespace. It requires an
// API token with a matching secret:read scope; every attempt is audited.
func (h *NodeHandler) GetSecret(ctx context.Context, req *connect.Request[spinneretv1.GetSecretRequest]) (*connect.Response[spinneretv1.GetSecretResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Kind != authz.KindToken {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "SecretService requires an API token; use SecretAdminService.RevealSecret")
	}
	_, ns, err := apiutil.Namespace(ctx, h.cat, "")
	if err != nil {
		return nil, err
	}
	v, err := h.secrets.ReadSecret(ctx, p, ns, req.Msg.GetPath(), int(req.Msg.GetVersion()), nodeReadPurpose)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetSecretResponse{
		Path:      v.Path,
		Version:   int32(v.Version),
		Value:     v.Value,
		ExpiresAt: apiutil.TimestampPtr(v.ExpiresAt),
	}), nil
}
