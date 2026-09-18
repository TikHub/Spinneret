// Package adminapi verifies protovalidate rules of the admin API messages.
package adminapi

import (
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

type vcase struct {
	name    string
	msg     proto.Message
	wantErr string // empty = valid; otherwise substring of the violation text
}

func run(t *testing.T, cases []vcase) {
	t.Helper()
	v, err := protovalidate.New()
	require.NoError(t, err)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.msg)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func ts(sec int64) *timestamppb.Timestamp { return timestamppb.New(time.Unix(sec, 0)) }

func ptr[T any](v T) *T { return &v }

const id = "idt_0192a3f4c1d27b8e9a01f2c3d4e5a6b7"

func TestIdentityAdmin(t *testing.T) {
	payload, err := structpb.NewStruct(map[string]any{"a": "b"})
	require.NoError(t, err)
	run(t, []vcase{
		{"list types ok", &v1.ListIdentityTypesRequest{Namespace: "prod"}, ""},
		{"list types no ns", &v1.ListIdentityTypesRequest{}, "namespace"},
		{"list types page too big", &v1.ListIdentityTypesRequest{Namespace: "prod", PageSize: 501}, "page_size"},
		{"list types negative page", &v1.ListIdentityTypesRequest{Namespace: "prod", PageSize: -1}, "page_size"},
		{"create type ok", &v1.CreateIdentityTypeRequest{Namespace: "prod", Site: "shop", SpecYaml: "name: x"}, ""},
		{"create type no yaml", &v1.CreateIdentityTypeRequest{Namespace: "prod", Site: "shop"}, "spec_yaml"},
		{"create type no site", &v1.CreateIdentityTypeRequest{Namespace: "prod", SpecYaml: "x"}, "site"},
		{"update type too big", &v1.UpdateIdentityTypeRequest{Id: id, SpecYaml: strings.Repeat("a", 1048577)}, "spec_yaml"},
		{"delete type no id", &v1.DeleteIdentityTypeRequest{}, "id"},
		{"preview type id", &v1.PreviewDeliveryRequest{Namespace: "prod", Source: &v1.PreviewDeliveryRequest_TypeId{TypeId: id}, PayloadJson: "{}"}, ""},
		{"preview yaml", &v1.PreviewDeliveryRequest{Namespace: "prod", Source: &v1.PreviewDeliveryRequest_SpecYaml{SpecYaml: "x"}, PayloadJson: "{}"}, ""},
		{"preview no source", &v1.PreviewDeliveryRequest{Namespace: "prod", PayloadJson: "{}"}, "source"},
		{"preview empty payload", &v1.PreviewDeliveryRequest{Namespace: "prod", Source: &v1.PreviewDeliveryRequest_TypeId{TypeId: id}}, "payload_json"},
		{"list identities ok", &v1.ListIdentitiesRequest{Namespace: "prod", OrderBy: "score", Filter: &v1.IdentityFilter{States: []string{"active", "banned"}, MinScore: ptr(10.0), MaxScore: ptr(20.0)}}, ""},
		{"list identities bad order", &v1.ListIdentitiesRequest{Namespace: "prod", OrderBy: "name"}, "order_by"},
		{"filter bad state", &v1.ListIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{States: []string{"alive"}}}, "states"},
		{"filter dup state", &v1.ListIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{States: []string{"active", "active"}}}, "states"},
		{"filter score range", &v1.ListIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{MinScore: ptr(50.0), MaxScore: ptr(10.0)}}, "min_score must not exceed max_score"},
		{"filter score > 100", &v1.ListIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{MaxScore: ptr(101.0)}}, "max_score"},
		{"filter min only zero", &v1.ListIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{MinScore: ptr(0.0)}}, ""},
		{"import ok", &v1.ImportIdentitiesRequest{Namespace: "prod", Site: "s", Type: "cookie", Format: "jsonl", Data: "{}"}, ""},
		{"import bad format", &v1.ImportIdentitiesRequest{Namespace: "prod", Site: "s", Type: "cookie", Format: "xml", Data: "{}"}, "format"},
		{"import bad mode", &v1.ImportIdentitiesRequest{Namespace: "prod", Site: "s", Type: "cookie", Format: "csv", Data: "a", Mode: "replace"}, "mode"},
		{"payload required", &v1.UpdateIdentityPayloadRequest{Id: id}, "payload"},
		{"payload ok", &v1.UpdateIdentityPayloadRequest{Id: id, Payload: payload}, ""},
		{"update identity label key empty", &v1.UpdateIdentityRequest{Id: id, Labels: map[string]string{"": "x"}, SetLabels: true}, "labels"},
		{"operate cooldown ok", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "30m"}, ""},
		{"operate cooldown compound", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "1d12h30m"}, ""},
		{"operate cooldown fractional", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "1.5h"}, ""},
		{"operate cooldown ms", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "500ms"}, ""},
		{"operate ban permanent", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "ban", Duration: "permanent"}, ""},
		{"operate ban no duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "ban"}, "duration is required"},
		{"operate cooldown garbage duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "soon"}, "duration"},
		{"operate cooldown negative duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "-5m"}, "duration"},
		{"operate ban zero duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "ban", Duration: "0"}, "greater than zero"},
		{"operate cooldown zero units", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "0d00h0.0m"}, "greater than zero"},
		{"operate cooldown half hour", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "0.5h"}, ""},
		{"operate cooldown permanent", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Duration: "permanent"}, "only allowed for ban"},
		{"operate quarantine permanent", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "quarantine", Duration: "permanent"}, "only allowed for ban"},
		{"operate quarantine default", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "quarantine"}, ""},
		{"operate unban ignores duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "unban", Duration: "permanent"}, ""},
		{"bulk ban zero", &v1.BulkOperateIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{}, Operation: "ban", Duration: "0s"}, "greater than zero"},
		{"account cooldown permanent", &v1.OperateAccountRequest{Id: "acc_1", Operation: "cooldown", Duration: "permanent"}, "only allowed for ban"},
		{"account ban permanent", &v1.OperateAccountRequest{Id: "acc_1", Operation: "ban", Duration: "permanent"}, ""},
		{"operate unban no duration", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "unban", ResetFailures: true}, ""},
		{"operate no ids", &v1.OperateIdentitiesRequest{Operation: "unban"}, "ids"},
		{"operate dup ids", &v1.OperateIdentitiesRequest{Ids: []string{id, id}, Operation: "unban"}, "ids"},
		{"operate too many ids", &v1.OperateIdentitiesRequest{Ids: manyIDs(1001), Operation: "unban"}, "ids"},
		{"operate bad op", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "delete"}, "operation"},
		{"operate endpoint scope needs eg", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Scope: "identity_endpoint", Duration: "5m"}, "endpoint_group_id is required"},
		{"operate endpoint scope ok", &v1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "cooldown", Scope: "identity_endpoint", EndpointGroupId: "eg_1", Duration: "5m"}, ""},
		{"bulk ok", &v1.BulkOperateIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{}, Operation: "disable", Limit: 100000}, ""},
		{"bulk no filter", &v1.BulkOperateIdentitiesRequest{Namespace: "prod", Operation: "disable"}, "filter"},
		{"bulk limit", &v1.BulkOperateIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{}, Operation: "disable", Limit: 100001}, "limit"},
		{"bulk bad duration", &v1.BulkOperateIdentitiesRequest{Namespace: "prod", Filter: &v1.IdentityFilter{}, Operation: "quarantine", Duration: "7 days"}, "duration"},
		{"revert ok", &v1.RevertActionsRequest{Namespace: "prod", Actions: []string{"ban"}, TimeRange: &v1.TimeRange{Start: ts(100), End: ts(200)}}, ""},
		{"revert open end ok", &v1.RevertActionsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{Start: ts(100)}}, ""},
		{"revert no range", &v1.RevertActionsRequest{Namespace: "prod"}, "time_range"},
		{"revert empty range", &v1.RevertActionsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{}}, "time_range.start is required"},
		{"revert inverted range", &v1.RevertActionsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{Start: ts(200), End: ts(100)}}, "start must be before"},
		{"revert bad action", &v1.RevertActionsRequest{Namespace: "prod", Actions: []string{"unban"}, TimeRange: &v1.TimeRange{Start: ts(1)}}, "actions"},
		{"events ok", &v1.ListStateEventsRequest{Namespace: "prod", SubjectKind: "proxy", Actions: []string{"rebind"}, Shadow: ptr(true)}, ""},
		{"events bad kind", &v1.ListStateEventsRequest{Namespace: "prod", SubjectKind: "user"}, "subject_kind"},
		{"events bad action", &v1.ListStateEventsRequest{Namespace: "prod", Actions: []string{"Ban!"}}, "actions"},
		{"events inverted range", &v1.ListStateEventsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{Start: ts(200), End: ts(100)}}, "start must be before"},
		{"accounts bad state", &v1.ListAccountsRequest{Namespace: "prod", State: "pending"}, "state"},
		{"upsert account ok", &v1.UpsertAccountRequest{Namespace: "prod", Site: "s", ExternalRef: "u1", Tags: []string{"a"}}, ""},
		{"upsert account no ref", &v1.UpsertAccountRequest{Namespace: "prod", Site: "s"}, "external_ref"},
		{"operate account ban", &v1.OperateAccountRequest{Id: "acc_1", Operation: "ban", Duration: "7d"}, ""},
		{"operate account cooldown no duration", &v1.OperateAccountRequest{Id: "acc_1", Operation: "cooldown"}, "duration is required"},
		{"operate account bad op", &v1.OperateAccountRequest{Id: "acc_1", Operation: "quarantine"}, "operation"},
		{"operate account bad duration", &v1.OperateAccountRequest{Id: "acc_1", Operation: "ban", Duration: "forever"}, "duration"},
	})
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "idt_" + strings.Repeat("0", 20) + time.Duration(i).String()
	}
	return out
}

func TestProxyAdmin(t *testing.T) {
	run(t, []vcase{
		{"list ok", &v1.ListProxiesRequest{Namespace: "prod", States: []string{"dead"}, Kinds: []string{"mobile"}}, ""},
		{"list bad kind", &v1.ListProxiesRequest{Namespace: "prod", Kinds: []string{"isp"}}, "kinds"},
		{"list bad state", &v1.ListProxiesRequest{Namespace: "prod", States: []string{"pending"}}, "states"},
		{"import ok", &v1.ImportProxiesRequest{Namespace: "prod", Format: "lines", Data: "http://h:1", Defaults: &v1.ProxyDefaults{Kind: "tunnel"}}, ""},
		{"import bad default kind", &v1.ImportProxiesRequest{Namespace: "prod", Format: "lines", Data: "x", Defaults: &v1.ProxyDefaults{Kind: "isp"}}, "kind"},
		{"import bad format", &v1.ImportProxiesRequest{Namespace: "prod", Format: "txt", Data: "x"}, "format"},
		{"import empty", &v1.ImportProxiesRequest{Namespace: "prod", Format: "lines"}, "data"},
		{"update ok", &v1.UpdateProxyRequest{Id: "pxy_1", MaxConcurrency: ptr(int32(5)), Kind: ptr("mobile")}, ""},
		{"update zero concurrency", &v1.UpdateProxyRequest{Id: "pxy_1", MaxConcurrency: ptr(int32(0))}, "max_concurrency"},
		{"update empty url", &v1.UpdateProxyRequest{Id: "pxy_1", Url: ptr("")}, "url"},
		{"update bad kind", &v1.UpdateProxyRequest{Id: "pxy_1", Kind: ptr("")}, "kind"},
		{"operate cooldown site", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "cooldown", Site: "shop", Duration: "10m"}, ""},
		{"operate cooldown no duration", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "cooldown"}, "duration is required"},
		{"operate bad duration", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "ban", Duration: "10 minutes"}, "duration"},
		{"operate proxy ban zero", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "ban", Duration: "0m"}, "greater than zero"},
		{"operate proxy quarantine permanent", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "quarantine", Duration: "permanent"}, "only allowed for ban"},
		{"operate proxy ban permanent", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "ban", Duration: "permanent"}, ""},
		{"operate bad op", &v1.OperateProxiesRequest{Ids: []string{"pxy_1"}, Operation: "expire"}, "operation"},
		{"delete none", &v1.DeleteProxiesRequest{}, "ids"},
		{"check ok", &v1.CheckProxyRequest{Id: "pxy_1"}, ""},
		{"provider stats ok", &v1.GetProviderStatsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{Start: ts(1), End: ts(2)}}, ""},
		{"provider stats inverted", &v1.GetProviderStatsRequest{Namespace: "prod", TimeRange: &v1.TimeRange{Start: ts(2), End: ts(1)}}, "start must be before"},
	})
}

func TestPolicyAdmin(t *testing.T) {
	report := &v1.Report{Uri: "/a", HttpStatus: 429}
	run(t, []vcase{
		{"list ok", &v1.ListPoliciesRequest{Namespace: "prod", Kind: "signal"}, ""},
		{"list bad kind", &v1.ListPoliciesRequest{Namespace: "prod", Kind: "quota"}, "kind"},
		{"create ok", &v1.CreatePolicyRequest{Namespace: "prod", Kind: "action", Yaml: "name: a", Publish: true}, ""},
		{"create no kind", &v1.CreatePolicyRequest{Namespace: "prod", Yaml: "name: a"}, "kind"},
		{"save draft empty", &v1.SaveDraftRequest{Id: "pol_1"}, "yaml"},
		{"publish negative expected", &v1.PublishPolicyRequest{Id: "pol_1", ExpectedVersion: -1}, "expected_version"},
		{"rollback zero", &v1.RollbackPolicyRequest{Id: "pol_1"}, "version"},
		{"diff ok", &v1.DiffPolicyVersionsRequest{Id: "pol_1", FromVersion: 1}, ""},
		{"binding ns level", &v1.SetBindingRequest{PolicyId: "pol_1"}, ""},
		{"binding eg level", &v1.SetBindingRequest{PolicyId: "pol_1", Site: "s", Client: "web", EndpointGroup: "search"}, ""},
		{"binding client without site", &v1.SetBindingRequest{PolicyId: "pol_1", Client: "web"}, "client requires site"},
		{"binding eg without client", &v1.SetBindingRequest{PolicyId: "pol_1", Site: "s", EndpointGroup: "search"}, "endpoint_group requires client"},
		{"resolve eg without client", &v1.ResolvePoliciesRequest{Namespace: "prod", Site: "s", EndpointGroup: "x"}, "endpoint_group requires client"},
		{"debug ok", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, Counts: map[string]int64{"identity:captcha:1h": 3}, BanCounts: map[string]int64{"30d": 1}}, ""},
		{"debug ok draft", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, DraftPolicyId: "pol_1", EndpointCooldownRemaining: "5m"}, ""},
		{"debug bad cooldown remaining", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, EndpointCooldownRemaining: "permanent"}, "endpoint_cooldown_remaining"},
		{"debug uri target", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, Target: &v1.DebugReportRequest_Uri{Uri: "/x"}}, ""},
		{"debug report nested rules ignored", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: &v1.Report{}}, ""},
		{"debug no report", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web"}, "report is required"},
		{"debug bad counts key", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, Counts: map[string]int64{"user:captcha:1h": 3}}, "counts"},
		{"debug negative count", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, Counts: map[string]int64{"identity:captcha:1h": -1}}, "counts"},
		{"debug bad state", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, IdentityState: "alive"}, "identity_state"},
		{"debug score range", &v1.DebugReportRequest{Namespace: "prod", Site: "s", Client: "web", Report: report, GlobalScore: 150}, "global_score"},
		{"validate ok", &v1.ValidatePolicyRequest{Kind: "breaker", Yaml: "x"}, ""},
		{"validate no kind", &v1.ValidatePolicyRequest{Yaml: "x"}, "kind"},
	})
}

func TestConfigAdmin(t *testing.T) {
	run(t, []vcase{
		{"list ok", &v1.ListConfigItemsRequest{Namespace: "prod", Group: "_runtime"}, ""},
		{"list empty group", &v1.ListConfigItemsRequest{Namespace: "prod"}, ""},
		{"list bad group", &v1.ListConfigItemsRequest{Namespace: "prod", Group: "-x"}, "group"},
		{"get by id", &v1.GetConfigItemRequest{Selector: &v1.GetConfigItemRequest_Id{Id: "cfg_1"}}, ""},
		{"get by locator", &v1.GetConfigItemRequest{Selector: &v1.GetConfigItemRequest_Locator{Locator: &v1.ConfigLocator{Namespace: "prod", Group: "_runtime", Key: "breakers"}}}, ""},
		{"get locator bad key", &v1.GetConfigItemRequest{Selector: &v1.GetConfigItemRequest_Locator{Locator: &v1.ConfigLocator{Namespace: "prod", Group: "crawler", Key: "/abs"}}}, "key"},
		{"get none", &v1.GetConfigItemRequest{}, "selector"},
		{"create ok", &v1.CreateConfigItemRequest{Namespace: "prod", Group: "crawler", Key: "search.json", Format: "json", Content: "{}"}, ""},
		{"create reserved group", &v1.CreateConfigItemRequest{Namespace: "prod", Group: "_runtime", Key: "x", Format: "json"}, "group"},
		{"create long group", &v1.CreateConfigItemRequest{Namespace: "prod", Group: "a" + strings.Repeat("b", 64), Key: "x", Format: "text"}, "group"},
		{"create bad format", &v1.CreateConfigItemRequest{Namespace: "prod", Group: "g", Key: "x", Format: "toml"}, "format"},
		{"create big content", &v1.CreateConfigItemRequest{Namespace: "prod", Group: "g", Key: "x", Format: "text", Content: strings.Repeat("a", 4194305)}, "content"},
		{"draft ok", &v1.SaveConfigDraftRequest{Id: "cfg_1", Content: "x", SchemaJson: ptr("")}, ""},
		{"publish ok", &v1.PublishConfigRequest{Id: "cfg_1", ExpectedVersion: 3}, ""},
		{"rollback zero", &v1.RollbackConfigRequest{Id: "cfg_1"}, "version"},
		{"diff negative", &v1.DiffConfigVersionsRequest{Id: "cfg_1", ToVersion: -1}, "to_version"},
	})
}

func TestSecretAdmin(t *testing.T) {
	run(t, []vcase{
		{"list ok", &v1.ListSecretsRequest{Namespace: "prod", Tags: []string{"a"}}, ""},
		{"get ok", &v1.SecretAdminServiceGetSecretRequest{Id: "sec_1"}, ""},
		{"create ok", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/api_key", Value: "v"}, ""},
		{"create dotted file ok", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/api_key.v2", Value: "v"}, ""},
		{"create max path", &v1.CreateSecretRequest{Namespace: "prod", Path: strings.Repeat("a", 256), Value: "v"}, ""},
		{"create path too long", &v1.CreateSecretRequest{Namespace: "prod", Path: strings.Repeat("a", 257), Value: "v"}, "path"},
		{"create upper path", &v1.CreateSecretRequest{Namespace: "prod", Path: "Signing/api_key", Value: "v"}, "path"},
		{"create dotdot path", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/../vendor/api_key", Value: "v"}, "path must not contain empty"},
		{"create dot path", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/./api_key", Value: "v"}, "path must not contain empty"},
		{"create trailing dotdot", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/..", Value: "v"}, "path must not contain empty"},
		{"create double slash", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing//api_key", Value: "v"}, "path must not contain empty"},
		{"create trailing slash", &v1.CreateSecretRequest{Namespace: "prod", Path: "signing/", Value: "v"}, "path must not contain empty"},
		{"create empty value", &v1.CreateSecretRequest{Namespace: "prod", Path: "a"}, "value"},
		{"create big value", &v1.CreateSecretRequest{Namespace: "prod", Path: "a", Value: strings.Repeat("x", 65537)}, "value"},
		{"update ok", &v1.UpdateSecretRequest{Id: "sec_1", Value: ptr("new"), ExpiresAt: ts(10)}, ""},
		{"update empty value", &v1.UpdateSecretRequest{Id: "sec_1", Value: ptr("")}, "value"},
		{"update expiry conflict", &v1.UpdateSecretRequest{Id: "sec_1", ExpiresAt: ts(10), ClearExpiresAt: true}, "mutually exclusive"},
		{"reveal negative", &v1.RevealSecretRequest{Id: "sec_1", Version: -1}, "version"},
		{"versions ok", &v1.ListSecretVersionsRequest{Id: "sec_1", PageSize: 500}, ""},
		{"logs no id", &v1.ListSecretAccessLogsRequest{}, "id"},
		{"kek status", &v1.GetKEKStatusRequest{}, ""},
		{"kek rewrap", &v1.StartKEKRewrapRequest{}, ""},
	})
}
