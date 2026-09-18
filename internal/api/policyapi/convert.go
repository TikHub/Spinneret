package policyapi

import (
	"math"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
)

func toPolicy(p policysvc.Policy) *spinneretv1.Policy {
	out := &spinneretv1.Policy{
		Id:             p.ID,
		Namespace:      p.NamespaceName,
		Kind:           string(p.Kind),
		Name:           p.Name,
		Description:    p.Description,
		CurrentVersion: clampInt32(p.CurrentVersion),
		PublishedYaml:  p.PublishedYAML,
		DraftYaml:      p.DraftYAML,
		HasDraft:       p.HasDraft,
		DraftUpdatedBy: p.DraftUpdatedBy,
		DraftUpdatedAt: apiutil.TimestampPtr(p.DraftUpdatedAt),
		Bindings:       make([]*spinneretv1.PolicyBinding, 0, len(p.Bindings)),
		CreatedBy:      p.CreatedBy,
		CreatedAt:      apiutil.Timestamp(p.CreatedAt),
		UpdatedAt:      apiutil.Timestamp(p.UpdatedAt),
	}
	for _, b := range p.Bindings {
		out.Bindings = append(out.Bindings, toBinding(b))
	}
	return out
}

func toBinding(b policysvc.Binding) *spinneretv1.PolicyBinding {
	return &spinneretv1.PolicyBinding{
		Id:              b.ID,
		PolicyId:        b.PolicyID,
		PolicyName:      b.PolicyName,
		Kind:            string(b.Kind),
		Namespace:       b.NamespaceName,
		Site:            b.SiteName,
		Client:          b.Client,
		EndpointGroup:   b.EndpointGroupName,
		EndpointGroupId: b.EndpointGroupID,
		Level:           string(b.Level),
		CreatedAt:       apiutil.Timestamp(b.CreatedAt),
	}
}

func toVersion(v policysvc.Version) *spinneretv1.PolicyVersion {
	return &spinneretv1.PolicyVersion{
		Version:   clampInt32(v.Version),
		SpecYaml:  v.SpecYAML,
		Comment:   v.Comment,
		CreatedBy: v.CreatedBy,
		CreatedAt: apiutil.Timestamp(v.CreatedAt),
	}
}

// debugInput converts a DebugReportRequest. The report is required; its
// field rules are enforced by the service because request validation skips it.
func debugInput(msg *spinneretv1.DebugReportRequest) (policysvc.DebugInput, error) {
	r := msg.GetReport()
	if r == nil {
		return policysvc.DebugInput{}, apperr.InvalidArgument("", "report is required")
	}
	return policysvc.DebugInput{
		Site:          msg.GetSite(),
		Client:        msg.GetClient(),
		EndpointGroup: msg.GetEndpointGroup(),
		URI:           msg.GetUri(),
		Report: policysvc.DebugReport{
			URI:           r.GetUri(),
			Method:        r.GetMethod(),
			HTTPStatus:    int(r.GetHttpStatus()),
			BusinessCode:  r.GetBusinessCode(),
			ErrorKind:     r.GetErrorKind(),
			Markers:       r.GetMarkers(),
			OutcomeHint:   r.GetOutcomeHint(),
			LatencyMs:     int64(r.GetLatencyMs()),
			ResponseBytes: r.GetResponseBytes(),
		},
		IdentityState:             msg.GetIdentityState(),
		Counts:                    msg.GetCounts(),
		HasAccount:                msg.GetHasAccount(),
		HasProxy:                  msg.GetHasProxy(),
		EndpointScore:             msg.GetEndpointScore(),
		EndpointSamples:           int(msg.GetEndpointSamples()),
		GlobalScore:               msg.GetGlobalScore(),
		GlobalSamples:             int(msg.GetGlobalSamples()),
		EndpointStreak:            int(msg.GetEndpointStreak()),
		SiteStreak:                int(msg.GetSiteStreak()),
		ProxyStreak:               int(msg.GetProxyStreak()),
		BanCounts:                 msg.GetBanCounts(),
		DraftPolicyID:             msg.GetDraftPolicyId(),
		EndpointCooldownRemaining: msg.GetEndpointCooldownRemaining(),
	}, nil
}

func toDebugResponse(res policysvc.DebugResult) *spinneretv1.DebugReportResponse {
	out := &spinneretv1.DebugReportResponse{
		Outcome:          res.Outcome,
		Blame:            res.Blame,
		MatchedRuleIndex: clampInt32(res.MatchedRuleIndex),
		MatchedRuleName:  res.MatchedRuleName,
		Actions:          make([]*spinneretv1.PlannedAction, 0, len(res.Actions)),
		Counters:         make([]*spinneretv1.CounterRequirement, 0, len(res.Counters)),
		Mode:             res.Mode,
	}
	for _, a := range res.Actions {
		out.Actions = append(out.Actions, &spinneretv1.PlannedAction{
			Action: a.Action, Scope: a.Scope, Duration: a.Duration, Permanent: a.Permanent,
			Severity: clampInt32(a.Severity), RuleName: a.RuleName, Source: a.Source, RuleIndex: clampInt32(a.RuleIndex),
		})
	}
	for _, c := range res.Counters {
		out.Counters = append(out.Counters, &spinneretv1.CounterRequirement{Subject: c.Subject, Outcome: c.Outcome, Window: c.Window})
	}
	return out
}

// clampInt32 converts an int, saturating at the int32 bounds.
func clampInt32(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	default:
		return int32(n)
	}
}
