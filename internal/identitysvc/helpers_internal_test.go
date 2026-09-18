package identitysvc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

func TestSetYAMLSite(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		site     string
		want     string
		verbatim bool
		reason   apperr.Reason
	}{
		{name: "same site kept verbatim", src: "name: a # keep\nsite: shop\n", site: "shop", verbatim: true},
		{name: "replaced", src: "name: a\nsite: other # comment\nclient: web\n", site: "shop", want: "name: a\nsite: shop\nclient: web\n"},
		{name: "inserted after name", src: "# head\nname: a\nclient: web\n", site: "shop", want: "# head\nname: a\nsite: shop\nclient: web\n"},
		{name: "inserted first without name", src: "client: web\n", site: "shop", want: "site: shop\nclient: web\n"},
		{name: "null site replaced", src: "name: a\nsite: ~\n", site: "shop", want: "name: a\nsite: shop\n"},
		{name: "mapping site replaced", src: "name: a\nsite: {x: 1}\n", site: "shop", want: "name: a\nsite: shop\n"},
		{name: "not a mapping", src: "[1, 2]\n", site: "shop", reason: apperr.ReasonInvalidArgument},
		{name: "invalid yaml", src: "name: [\n", site: "shop", reason: apperr.ReasonInvalidArgument},
		{name: "empty", src: "\n", site: "shop", reason: apperr.ReasonInvalidArgument},
		{name: "multiple documents", src: "a: 1\n---\nb: 2\n", site: "shop", reason: apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := setYAMLSite([]byte(tc.src), tc.site)
			if tc.reason != "" {
				require.Equal(t, tc.reason, apperr.ReasonOf(err))
				return
			}
			require.NoError(t, err)
			if tc.verbatim {
				require.Equal(t, tc.src, string(got))
				return
			}
			require.Equal(t, strings.TrimSpace(tc.want), strings.TrimSpace(string(got)))
			require.Equal(t, tc.site, yamlSiteName(got))
		})
	}
	require.Empty(t, yamlSiteName([]byte("::")))
	require.Empty(t, yamlSiteName([]byte("site: {a: b}")))
}

func TestStateAfterPayloadUpdate(t *testing.T) {
	tests := []struct {
		state, activation, want string
	}{
		{StateActive, identity.ActivationProbe, StatePending},
		{StatePending, identity.ActivationProbe, StatePending},
		{StateQuarantined, identity.ActivationProbe, StatePending},
		{StateExpired, identity.ActivationProbe, StatePending},
		{StateExpired, identity.ActivationImmediate, StateActive},
		{StateActive, identity.ActivationImmediate, StateActive},
		{StateBanned, identity.ActivationImmediate, StateBanned},
		{StateDisabled, identity.ActivationProbe, StateDisabled},
		{StateRetired, identity.ActivationImmediate, StateRetired},
	}
	for _, tc := range tests {
		t.Run(tc.state+"/"+tc.activation, func(t *testing.T) {
			require.Equal(t, tc.want, stateAfterPayloadUpdate(tc.state, tc.activation))
		})
	}
	require.Equal(t, StateActive, initialState(identity.ActivationImmediate))
	require.Equal(t, StatePending, initialState(identity.ActivationProbe))
	require.Equal(t, int32(1), pruneFloor(3))
	require.Equal(t, int32(4), pruneFloor(8))
}

func TestApplyPayloadChange(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	w := identitysvcdb.IdentityWriteParams{ID: "idt_1", State: StateQuarantined, QuarantineUntil: &until, PayloadVersion: 4}
	p := preparedPayload{uniqueHash: []byte("u"), payloadHash: []byte("p")}
	tr := applyPayloadChange(&w, p, identity.ActivationImmediate, now)
	require.Equal(t, &transition{IdentityID: "idt_1", From: StateQuarantined, To: StateActive}, tr)
	require.Equal(t, int32(5), w.PayloadVersion)
	require.Nil(t, w.QuarantineUntil)
	require.Equal(t, now, *w.ActivatedAt)
	require.Equal(t, now, w.StateChangedAt)
	require.Equal(t, payloadUpdatedReason, w.StateReason)

	w = identitysvcdb.IdentityWriteParams{ID: "idt_2", State: StateBanned, PayloadVersion: 1}
	require.Nil(t, applyPayloadChange(&w, p, identity.ActivationProbe, now))
	require.Equal(t, StateBanned, w.State)
}

func TestApplyImportAttributes(t *testing.T) {
	acc := "acc_old"
	base := identitysvcdb.IdentityWriteParams{AccountID: &acc, Region: "US", Tags: []string{"a"}, Labels: json.RawMessage(`{"k": "v"}`)}
	accounts := map[string]string{"alice": "acc_new", "old": "acc_old"}
	tests := []struct {
		name    string
		row     importRow
		changed bool
	}{
		{"nothing set", importRow{}, false},
		{"same values", importRow{account: "old", region: "US", tags: []string{"a"}, labels: map[string]string{"k": "v"}}, false},
		{"account", importRow{account: "alice"}, true},
		{"unknown account ignored", importRow{account: "ghost"}, false},
		{"region", importRow{region: "JP"}, true},
		{"tags cleared", importRow{tags: []string{}}, true},
		{"labels", importRow{labels: map[string]string{"k": "w"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := base
			require.Equal(t, tc.changed, applyImportAttributes(&w, &tc.row, accounts))
		})
	}
}

func TestLabelsHelpers(t *testing.T) {
	require.True(t, sameLabels(json.RawMessage(`{}`), nil))
	require.True(t, sameLabels(nil, map[string]string{}))
	require.False(t, sameLabels(json.RawMessage(`not json`), map[string]string{}))
	require.False(t, sameLabels(json.RawMessage(`{"a":"1"}`), map[string]string{"a": "2"}))
	require.False(t, sameLabels(json.RawMessage(`{"a":"1"}`), map[string]string{"b": "1"}))
	require.JSONEq(t, `{}`, string(labelsJSON(nil)))
	require.JSONEq(t, `{"a":"b"}`, string(labelsJSON(map[string]string{"a": "b"})))
	require.Equal(t, map[string]string{"a": "b"}, decodeLabels(json.RawMessage(`{"a":"b","n":1}`)))
	require.Empty(t, decodeLabels(json.RawMessage(`[]`)))
}

func TestValidationHelpers(t *testing.T) {
	require.Equal(t, "abc", truncateText("abc", 10))
	require.Equal(t, "ab…", truncateText("abcdef", 2))
	require.Equal(t, "…", truncateText("日本", 2), "never splits a rune")
	require.Equal(t, DefaultPageSize, pageSize(0))
	require.Equal(t, MaxPageSize, pageSize(10_000))
	require.Equal(t, 7, pageSize(7))

	var cur typeCursor
	ok, err := decodeCursor("", &cur)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = decodeCursor(strings.Repeat("a", MaxPageTokenBytes+1), &cur)
	require.Error(t, err)
	ok, err = decodeCursor(encodeCursor(typeCursor{After: "x"}), &cur)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "x", cur.After)
	require.Empty(t, encodeCursor(func() {}))

	tags, err := validateTags([]string{"a", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, tags)
	require.Error(t, validateLabels(map[string]string{"k": "\xff"}))
	require.Error(t, validateText("x", "\xff", 10))
	require.Equal(t, []string{"a", "b"}, dedupe([]string{"", "a", "b", "a"}))

	ops := []struct {
		req OperationRequest
		ok  bool
	}{
		{OperationRequest{Operation: "ban", Duration: durationx.Permanent}, true},
		{OperationRequest{Operation: "ban", Duration: durationx.Duration(time.Hour)}, true},
		{OperationRequest{Operation: "quarantine"}, true},
		{OperationRequest{Operation: "cooldown", Scope: "identity_site", Duration: durationx.Duration(time.Minute)}, true},
		{OperationRequest{Operation: "cooldown", Scope: "identity_endpoint", EndpointGroupID: strings.Repeat("e", 65), Duration: durationx.Duration(time.Minute)}, false},
	}
	for _, tc := range ops {
		err := validateOperation(tc.req, identityOperations, true)
		require.Equal(t, tc.ok, err == nil, "%+v: %v", tc.req, err)
	}
	now := time.Now()
	req := RevertRequest{From: now.Add(-time.Hour), Actions: []string{"ban", "ban"}}
	require.NoError(t, validateRevert(&req, now))
	require.Equal(t, []string{"ban"}, req.Actions)
	require.Error(t, validateRevert(&RevertRequest{From: now.Add(-time.Hour), Rule: "\x00"}, now))
	require.Equal(t, "a", stripNewlines("\na\r\t"))
}

func TestRelatedToNamespace(t *testing.T) {
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "default")
	other := &catalog.Namespace{ID: "ns_2", TenantID: "ten_1"}
	tests := []struct {
		name string
		p    *authz.Principal
		ns   *catalog.Namespace
		want bool
	}{
		{"nil", nil, ns, false},
		{"system", authz.System("x"), ns, true},
		{"platform admin", &authz.Principal{Kind: authz.KindUser, IsPlatformAdmin: true}, ns, true},
		{"tenant binding", &authz.Principal{Kind: authz.KindUser, Bindings: []authz.Binding{{TenantID: "ten_1", Role: authz.RoleViewer}}}, ns, true},
		{"other namespace binding", &authz.Principal{Kind: authz.KindUser, Bindings: []authz.Binding{{TenantID: "ten_1", NamespaceID: "ns_2", Role: authz.RoleViewer}}}, ns, false},
		{"token", &authz.Principal{Kind: authz.KindToken, TenantID: "ten_1", NamespaceID: "ns_1"}, ns, true},
		{"token of other namespace", &authz.Principal{Kind: authz.KindToken, TenantID: "ten_1", NamespaceID: "ns_1"}, other, false},
		{"unknown kind", &authz.Principal{Kind: "robot"}, ns, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, relatedToNamespace(tc.p, tc.ns))
		})
	}
	_, err := visibleSites(nil, ns, authz.PermIdentityRead)
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))
	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, _, err = svc.siteByID("sit_1", "identity")
	require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))
	svc.invalidateCatalog(t.Context(), "ns_1")
	require.True(t, svc.syncHot(t.Context(), "sit_1", []syncGroup{{ids: []string{"idt_1"}}}, []string{"acc_1"}))
	svc.publishTransitions(t.Context(), ns, "sit_1", []transition{{IdentityID: "idt_1"}})
}
