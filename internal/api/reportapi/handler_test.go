package reportapi

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/signal"
	"github.com/TikHub/Spinneret/internal/testutil"
)

const (
	tenantID = "ten_1"
	nsID     = "ns_1"
	siteKey  = 31
)

type env struct {
	h     *Handler
	cat   *catalogtest.Catalog
	ns    *catalog.Namespace
	lease string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	ns := catalogtest.NewNamespace(tenantID, nsID, "prod")
	catalogtest.AddSite(ns, "sit_1", "shop", siteKey, "web")
	catalogtest.AddSite(ns, "sit_2", "market", siteKey+1, "web")
	cat := catalogtest.New(ns)
	lease := idgen.LeasePrefix(siteKey) + idgen.FormatShard(1)
	require.NoError(t, rdb.Do(context.Background(), rdb.B().Hset().Key(keys.Lease(siteKey, lease)).FieldValue().
		FieldValue("ns", nsID).FieldValue("st", "active").Build()).Error())
	ing := signal.NewIngestor(signal.Config{ReportShards: 16}, rdb, keys, cat, nil, nil, nil)
	return &env{h: New(ing, cat), cat: cat, ns: ns, lease: lease}
}

func token(t *testing.T, scopes ...string) *authz.Principal {
	t.Helper()
	sc, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{Kind: authz.KindToken, ID: "tok_1", TenantID: tenantID, NamespaceID: nsID,
		NamespaceName: "prod", Scopes: sc, Node: "crawler-01"}
}

func request(reports ...*spinneretv1.Report) *connect.Request[spinneretv1.ReportRequest] {
	return connect.NewRequest(&spinneretv1.ReportRequest{Reports: reports})
}

func report(id, lease string) *spinneretv1.Report {
	now := time.Now()
	return &spinneretv1.Report{
		ReportId: id, LeaseId: lease, Uri: "/search", HttpStatus: 200,
		StartedAt: timestamppb.New(now.Add(-time.Second)), FinishedAt: timestamppb.New(now),
	}
}

func TestReport(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithPrincipal(context.Background(), token(t, "report:write"))
	resp, err := e.h.Report(ctx, request(report("r1", e.lease), report("r1", e.lease), report("r2", "lse_bad")))
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.Msg.GetAccepted())
	require.EqualValues(t, 1, resp.Msg.GetDuplicated())
	require.Len(t, resp.Msg.GetRejected(), 1)
	rej := resp.Msg.GetRejected()[0]
	require.Equal(t, "r2", rej.GetReportId())
	require.Equal(t, string(apperr.ReasonLeaseUnknown), rej.GetReason())
	require.NotEmpty(t, rej.GetMessage())

	resp, err = e.h.Report(ctx, request(report("r1", e.lease)))
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.Msg.GetDuplicated())
	require.NotNil(t, resp.Msg.GetRejected())
}

func TestReportPermissions(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name      string
		principal *authz.Principal
		reason    apperr.Reason // "" = allowed
		rejected  apperr.Reason
	}{
		{name: "unauthenticated", reason: apperr.ReasonSessionInvalid},
		{name: "token without report scope", principal: token(t, "lease:acquire"), reason: apperr.ReasonScopeMissing},
		{name: "token scoped to another site", principal: token(t, "report:write:market"), rejected: apperr.ReasonScopeMissing},
		{name: "token scoped to the lease site", principal: token(t, "report:write:shop")},
		{name: "token of an unknown namespace", principal: func() *authz.Principal {
			p := token(t, "lease:acquire")
			p.NamespaceID = "ns_gone"
			return p
		}(), reason: apperr.ReasonScopeMissing},
		{name: "owner user", principal: &authz.Principal{Kind: authz.KindUser, ID: "usr_1", TenantID: tenantID,
			Bindings: []authz.Binding{{TenantID: tenantID, Role: authz.RoleOwner}}}, reason: apperr.ReasonPermissionDenied},
		{name: "platform admin", principal: &authz.Principal{Kind: authz.KindUser, ID: "usr_2", IsPlatformAdmin: true}},
		{name: "system", principal: authz.System("test")},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.principal != nil {
				ctx = authz.WithPrincipal(ctx, tt.principal)
			}
			resp, err := e.h.Report(ctx, request(report("perm-"+strconv.Itoa(i), e.lease)))
			if tt.reason != "" {
				require.Error(t, err)
				require.Equal(t, tt.reason, apperr.ReasonOf(err))
				return
			}
			require.NoError(t, err)
			if tt.rejected != "" {
				require.Len(t, resp.Msg.GetRejected(), 1)
				require.Equal(t, string(tt.rejected), resp.Msg.GetRejected()[0].GetReason())
				return
			}
			require.EqualValues(t, 1, resp.Msg.GetAccepted())
		})
	}
}

func TestReportBatchLimits(t *testing.T) {
	e := newEnv(t)
	ctx := authz.WithPrincipal(context.Background(), token(t, "report:write"))
	_, err := e.h.Report(ctx, request())
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	many := make([]*spinneretv1.Report, signal.MaxReports+1)
	_, err = e.h.Report(ctx, request(many...))
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
}

type failingIngester struct{}

func (failingIngester) Ingest(context.Context, *authz.Principal, string, []*spinneretv1.Report) (int, int, []signal.Rejected, error) {
	return 0, 0, nil, apperr.Unavailable(apperr.ReasonInternal, 1000, "down").WithCause(errors.New("redis down"))
}

func TestReportIngestError(t *testing.T) {
	ns := catalogtest.NewNamespace(tenantID, nsID, "prod")
	h := New(failingIngester{}, catalogtest.New(ns))
	ctx := authz.WithPrincipal(context.Background(), token(t, "report:write"))
	_, err := h.Report(ctx, request(report("r", "l")))
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok)
	require.Equal(t, connect.CodeUnavailable, e.Code)
}
