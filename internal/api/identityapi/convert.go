package identityapi

import (
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
)

// identityTypeProto converts an identity type.
func identityTypeProto(t identitysvc.IdentityType) *spinneretv1.IdentityType {
	out := &spinneretv1.IdentityType{
		Id: t.ID, Namespace: t.NamespaceName, Site: t.SiteName, Client: t.Client, Name: t.Name,
		Description: t.Description, SpecYaml: t.SpecYAML, JsonSchema: string(t.JSONSchema), Version: clampInt32(t.Version),
		IdentityCount: clampInt32(t.IdentityCount), Fields: []*spinneretv1.IdentityField{}, UniqueBy: []string{},
		CreatedAt: apiutil.Timestamp(t.CreatedAt), UpdatedAt: apiutil.Timestamp(t.UpdatedAt),
	}
	if t.Spec == nil {
		return out
	}
	names := make([]string, 0, len(t.Spec.Fields))
	for name := range t.Spec.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := t.Spec.Fields[name]
		out.Fields = append(out.Fields, &spinneretv1.IdentityField{
			Name: name, Type: string(f.Type), Required: f.Required, Sensitive: f.Sensitive, Description: f.Description,
		})
	}
	out.UniqueBy = append(out.UniqueBy, t.Spec.UniqueBy...)
	out.Activation = t.Spec.Activation
	return out
}

// identityProto converts an identity.
func identityProto(i identitysvc.Identity) *spinneretv1.Identity {
	out := &spinneretv1.Identity{
		Id: i.ID, Namespace: i.NamespaceName, Site: i.SiteName, Client: i.Client, Type: i.TypeName, TypeId: i.TypeID,
		AccountId: i.AccountID, AccountRef: i.AccountRef, State: i.State, StateReason: i.StateReason,
		StateChangedAt: apiutil.Timestamp(i.StateChangedAt), BanUntil: apiutil.TimestampPtr(i.BanUntil),
		QuarantineUntil: apiutil.TimestampPtr(i.QuarantineUntil), Region: i.Region, Tags: i.Tags, Labels: i.Labels,
		PayloadVersion: clampInt32(i.PayloadVersion), ActivatedAt: apiutil.TimestampPtr(i.ActivatedAt),
		LastUsedAt: apiutil.TimestampPtr(i.LastUsedAt), GlobalScore: i.GlobalScore,
		GlobalSamples: clampInt32(i.GlobalSamples), BoundProxyId: i.BoundProxyID,
		CreatedAt: apiutil.Timestamp(i.CreatedAt), UpdatedAt: apiutil.Timestamp(i.UpdatedAt),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return out
}

// accountProto converts an account.
func accountProto(a identitysvc.Account) *spinneretv1.Account {
	out := &spinneretv1.Account{
		Id: a.ID, Site: a.SiteName, ExternalRef: a.ExternalRef, Region: a.Region, Tags: a.Tags, State: a.State,
		BanUntil: apiutil.TimestampPtr(a.BanUntil), CooldownUntil: apiutil.TimestampPtr(a.CooldownUntil), Notes: a.Notes,
		IdentityCount: clampInt32(a.IdentityCount), CreatedAt: apiutil.Timestamp(a.CreatedAt),
		UpdatedAt: apiutil.Timestamp(a.UpdatedAt),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	return out
}

// stateEventProto converts a state event.
func stateEventProto(ev identitysvc.StateEvent) *spinneretv1.StateEvent {
	return &spinneretv1.StateEvent{
		Id: ev.ID, CreatedAt: apiutil.Timestamp(ev.CreatedAt), Namespace: ev.NamespaceName, Site: ev.SiteName,
		SubjectKind: ev.SubjectKind, SubjectId: ev.SubjectID, EndpointGroup: ev.EndpointGroupName,
		FromState: ev.FromState, ToState: ev.ToState, Action: ev.Action, Scope: ev.Scope,
		Until: apiutil.TimestampPtr(ev.Until), Permanent: ev.Permanent, Outcome: ev.Outcome, PolicyId: ev.PolicyID,
		PolicyVersion: clampInt32(ev.PolicyVersion), Rule: ev.Rule, ReportId: ev.ReportID, LeaseId: ev.LeaseID,
		Actor: ev.Actor, Reason: ev.Reason, Shadow: ev.Shadow,
	}
}

// bulkResultProto converts a bulk operation result.
func bulkResultProto(r identitysvc.BulkResult) *spinneretv1.BulkResult {
	out := &spinneretv1.BulkResult{
		Matched: clampInt32(r.Matched), Succeeded: clampInt32(r.Succeeded),
		Failed: make([]*spinneretv1.BulkFailure, 0, len(r.Failed)),
	}
	for _, f := range r.Failed {
		out.Failed = append(out.Failed, &spinneretv1.BulkFailure{Id: f.ID, Reason: f.Reason, Message: f.Message})
	}
	return out
}

// hotStateProto converts the live hot state of an identity.
func hotStateProto(h identitysvc.HotState) *spinneretv1.IdentityHotState {
	out := &spinneretv1.IdentityHotState{
		Present: h.Present, State: h.State, ActiveLeases: clampInt32(h.ActiveLeases),
		SiteCooldownUntil: apiutil.Timestamp(h.SiteCooldownUntil), SiteReuseUntil: apiutil.Timestamp(h.SiteReuseUntil),
		ExclusiveUntil: apiutil.Timestamp(h.ExclusiveUntil), BoundProxyId: h.BoundProxyID, GlobalScore: h.GlobalScore,
		GlobalSamples: clampInt32(h.GlobalSamples), AccountCooldownUntil: apiutil.Timestamp(h.AccountCooldownUntil),
		Groups: make([]*spinneretv1.EndpointHotState, 0, len(h.Groups)),
	}
	for _, g := range h.Groups {
		out.Groups = append(out.Groups, &spinneretv1.EndpointHotState{
			EndpointGroup: g.EndpointGroup, EndpointGroupId: g.EndpointGroupID, Client: g.Client, Score: g.Score,
			Samples: clampInt32(g.Samples), ConsecutiveFailures: clampInt32(g.ConsecutiveFailures),
			CooldownUntil: apiutil.Timestamp(g.CooldownUntil), ReuseUntil: apiutil.Timestamp(g.ReuseUntil),
			LastUsedAt: apiutil.Timestamp(g.LastUsedAt), AvailableAt: apiutil.Timestamp(g.AvailableAt),
			InReadyQueue: g.InReadyQueue,
		})
	}
	return out
}

// credentialProto converts a rendered credential. Rendered values only use
// JSON value types, so a conversion failure indicates a bug and is reported
// without the offending value.
func credentialProto(c *identity.Credential) (*spinneretv1.Credential, error) {
	out := &spinneretv1.Credential{
		Cookies: c.Cookies, CookieHeader: c.CookieHeader, Headers: c.Headers, Query: c.Query,
	}
	if c.JSON != nil {
		v, err := structpb.NewValue(c.JSON)
		if err != nil {
			return nil, fmt.Errorf("json segment cannot be converted: %T", c.JSON)
		}
		out.Json = v
	}
	values, err := structpb.NewStruct(c.Values)
	if err != nil {
		return nil, fmt.Errorf("values segment cannot be converted")
	}
	out.Values = values
	return out, nil
}

// filterFromProto converts an identity filter (nil = match all non-retired).
func filterFromProto(f *spinneretv1.IdentityFilter) identitysvc.IdentityFilter {
	if f == nil {
		return identitysvc.IdentityFilter{}
	}
	return identitysvc.IdentityFilter{
		Site: f.GetSite(), Type: f.GetType(), States: f.GetStates(), Tags: f.GetTags(), AccountRef: f.GetAccountRef(),
		Region: f.GetRegion(), Search: f.GetSearch(), MinScore: f.MinScore, MaxScore: f.MaxScore,
		IncludeRetired: f.GetIncludeRetired(),
	}
}

// timeRange converts an optional time range; unset bounds are zero times.
func timeRange(r *spinneretv1.TimeRange) (from, to time.Time, err error) {
	if r == nil {
		return time.Time{}, time.Time{}, nil
	}
	if ts := r.GetStart(); ts != nil {
		if err := ts.CheckValid(); err != nil {
			return time.Time{}, time.Time{}, apperr.InvalidArgument("", "time_range.start is invalid")
		}
		from = ts.AsTime()
	}
	if ts := r.GetEnd(); ts != nil {
		if err := ts.CheckValid(); err != nil {
			return time.Time{}, time.Time{}, apperr.InvalidArgument("", "time_range.end is invalid")
		}
		to = ts.AsTime()
	}
	return from, to, nil
}
