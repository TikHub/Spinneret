package policysvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvcdb"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// Debug input limits (mirroring the Report and DebugReportRequest messages).
const (
	maxDebugCounts       = 256
	maxDebugBanCounts    = 32
	maxDebugMarkers      = 32
	maxDebugMarkerLength = 64
	maxDebugStreak       = 1_000_000
	maxBusinessCodeLen   = 64
	maxMethodLength      = 16
	maxOutcomeHintLength = 32
	maxHTTPStatus        = 999
)

// validIdentityStates lists the identity states accepted by DebugReport.
var validIdentityStates = []string{"pending", "active", "expired", "banned", "quarantined", "disabled", "retired"}

// DebugReport classifies a sample report with the signal policy of the
// endpoint group and plans actions with its action policy, without touching
// Redis (policy:read on the site). With DraftPolicyID the draft (or, without
// a draft, the published YAML) of that signal or action policy replaces the
// resolved policy of its kind.
func (s *Service) DebugReport(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in DebugInput) (DebugResult, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return DebugResult{}, err
	}
	// Check access before resolving names so that callers without any
	// policy:read in the namespace cannot probe site and group names.
	if !accessFor(p, ns, authz.PermPolicyRead).any() {
		return DebugResult{}, denied(p, ns, authz.PermPolicyRead)
	}
	st, ok := ns.Sites[in.Site]
	if !ok {
		return DebugResult{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", in.Site)
	}
	if err := p.Require(authz.PermPolicyRead, siteResource(ns, st.ID)); err != nil {
		return DebugResult{}, err
	}
	if !st.HasClient(in.Client) {
		return DebugResult{}, apperr.InvalidArgument(apperr.ReasonClientUnknown, "site %q has no client %q", st.Name, in.Client)
	}
	eval, err := debugEvalInput(in)
	if err != nil {
		return DebugResult{}, err
	}
	facts, group, err := debugTarget(st, in)
	if err != nil {
		return DebugResult{}, err
	}
	sig, act := group.Signal, group.Action
	if in.DraftPolicyID != "" {
		if sig, act, err = s.draftPolicies(ctx, p, ns, in.DraftPolicyID, sig, act); err != nil {
			return DebugResult{}, err
		}
	}
	if sig == nil || act == nil {
		return DebugResult{}, apperr.FailedPrecondition("", "policies of endpoint group %q are not loaded", group.Name)
	}
	cls := sig.Classify(facts)
	counters := act.CounterRequests(cls.Outcome)
	counts, err := parseCounts(in.Counts)
	if err != nil {
		return DebugResult{}, err
	}
	for _, c := range counters {
		if _, ok := counts[c]; !ok {
			counts[c] = 1
		}
	}
	eval.Outcome, eval.Blame, eval.Counts = cls.Outcome, cls.Blame, counts
	planned := act.Evaluate(eval)
	out := DebugResult{
		EndpointGroupID:   group.ID,
		EndpointGroupName: group.Name,
		Outcome:           cls.Outcome,
		Blame:             string(cls.Blame),
		MatchedRuleIndex:  cls.RuleIndex,
		MatchedRuleName:   cls.RuleName,
		Mode:              act.Mode,
		Actions:           make([]PlannedAction, 0, len(planned)),
		Counters:          make([]CounterRequirement, 0, len(counters)),
	}
	for _, a := range planned {
		out.Actions = append(out.Actions, PlannedAction{
			Action: string(a.Action), Scope: string(a.Scope), Duration: formatDuration(a), Permanent: a.Permanent,
			Severity: a.Severity, RuleName: a.RuleName, Source: a.Source, RuleIndex: a.RuleIndex,
		})
	}
	for _, c := range counters {
		out.Counters = append(out.Counters, CounterRequirement{
			Subject: string(c.Subject), Outcome: c.Outcome, Window: durationx.Duration(c.Window).String(),
		})
	}
	return out, nil
}

// debugTarget validates the report facts and selects the endpoint group.
func debugTarget(st *catalog.Site, in DebugInput) (policy.ReportFacts, *catalog.EndpointGroup, error) {
	r := in.Report
	if err := validateDebugReport(r); err != nil {
		return policy.ReportFacts{}, nil, err
	}
	facts := policy.ReportFacts{
		Method: r.Method, HTTPStatus: r.HTTPStatus, BusinessCode: r.BusinessCode, ErrorKind: r.ErrorKind,
		Markers: r.Markers, OutcomeHint: r.OutcomeHint, LatencyMs: r.LatencyMs, ResponseBytes: r.ResponseBytes,
	}
	if r.URI != "" {
		path, err := site.NormalizePath(r.URI)
		if err != nil {
			return policy.ReportFacts{}, nil, err
		}
		facts.URI = path
	}
	if in.EndpointGroup != "" {
		g, ok := st.Group(in.Client, in.EndpointGroup)
		if !ok {
			return policy.ReportFacts{}, nil, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown,
				"endpoint group %q not found for site %q client %q", in.EndpointGroup, st.Name, in.Client)
		}
		return facts, g, nil
	}
	matchPath := facts.URI
	if in.URI != "" {
		path, err := site.NormalizePath(in.URI)
		if err != nil {
			return policy.ReportFacts{}, nil, err
		}
		matchPath = path
		if facts.URI == "" {
			facts.URI = path
		}
	}
	if matchPath == "" {
		return policy.ReportFacts{}, nil, apperr.InvalidArgument(apperr.ReasonURIInvalid, "report.uri is required when no endpoint group or uri is given")
	}
	g, _, ok := st.MatchGroup(in.Client, matchPath)
	if !ok {
		return policy.ReportFacts{}, nil, apperr.FailedPrecondition("", "site %q client %q has no default endpoint group", st.Name, in.Client)
	}
	return facts, g, nil
}

// validateDebugReport enforces the Report field rules that request validation
// skips for DebugReportRequest.report.
func validateDebugReport(r DebugReport) error {
	// Lengths are counted in characters, like protovalidate max_len.
	switch {
	case utf8.RuneCountInString(r.URI) > site.MaxPathLength:
		return apperr.InvalidArgument(apperr.ReasonURIInvalid, "report.uri must be at most %d characters", site.MaxPathLength)
	case utf8.RuneCountInString(r.Method) > maxMethodLength:
		return apperr.InvalidArgument("", "report.method must be at most %d characters", maxMethodLength)
	case r.HTTPStatus < 0 || r.HTTPStatus > maxHTTPStatus:
		return apperr.InvalidArgument("", "report.http_status must be between 0 and %d", maxHTTPStatus)
	case utf8.RuneCountInString(r.BusinessCode) > maxBusinessCodeLen:
		return apperr.InvalidArgument("", "report.business_code must be at most %d characters", maxBusinessCodeLen)
	case r.ErrorKind != "" && r.ErrorKind != "other" && !policy.ValidErrorKind(r.ErrorKind):
		return apperr.InvalidArgument("", "report.error_kind must be one of %s|other", strings.Join(policy.ErrorKinds(), "|"))
	case len(r.Markers) > maxDebugMarkers:
		return apperr.InvalidArgument("", "report.markers must have at most %d items", maxDebugMarkers)
	case utf8.RuneCountInString(r.OutcomeHint) > maxOutcomeHintLength:
		return apperr.InvalidArgument("", "report.outcome_hint must be at most %d characters", maxOutcomeHintLength)
	case r.LatencyMs < 0 || r.ResponseBytes < 0:
		return apperr.InvalidArgument("", "report.latency_ms and report.response_bytes must not be negative")
	}
	for i, m := range r.Markers {
		if m == "" || utf8.RuneCountInString(m) > maxDebugMarkerLength {
			return apperr.InvalidArgument("", "report.markers[%d] must be 1..%d characters", i, maxDebugMarkerLength)
		}
	}
	return nil
}

// debugEvalInput validates the assumed hot state.
func debugEvalInput(in DebugInput) (policy.EvalInput, error) {
	state := in.IdentityState
	if state == "" {
		state = "active"
	}
	if !slices.Contains(validIdentityStates, state) {
		return policy.EvalInput{}, apperr.InvalidArgument("", "identity_state must be one of %s", strings.Join(validIdentityStates, "|"))
	}
	for _, v := range []float64{in.EndpointScore, in.GlobalScore} {
		if !(v >= 0 && v <= 100) {
			return policy.EvalInput{}, apperr.InvalidArgument("", "scores must be between 0 and 100")
		}
	}
	for _, v := range []int{in.EndpointSamples, in.GlobalSamples, in.EndpointStreak, in.SiteStreak, in.ProxyStreak} {
		if v < 0 {
			return policy.EvalInput{}, apperr.InvalidArgument("", "samples and streaks must not be negative")
		}
	}
	for _, v := range []int{in.EndpointStreak, in.SiteStreak, in.ProxyStreak} {
		if v > maxDebugStreak {
			return policy.EvalInput{}, apperr.InvalidArgument("", "streaks must be at most %d", maxDebugStreak)
		}
	}
	remaining, err := parseWindow("endpoint_cooldown_remaining", in.EndpointCooldownRemaining, true)
	if err != nil {
		return policy.EvalInput{}, err
	}
	bans, err := parseBanCounts(in.BanCounts)
	if err != nil {
		return policy.EvalInput{}, err
	}
	siteStreak := in.SiteStreak
	if siteStreak == 0 {
		siteStreak = in.EndpointStreak
	}
	return policy.EvalInput{
		IdentityState: state, HasAccount: in.HasAccount, HasProxy: in.HasProxy, BanCounts: bans,
		EndpointStreak: in.EndpointStreak, SiteStreak: siteStreak, ProxyStreak: in.ProxyStreak,
		EndpointScore: in.EndpointScore, EndpointSamples: in.EndpointSamples,
		GlobalScore: in.GlobalScore, GlobalSamples: in.GlobalSamples,
		EndpointCooldownRemaining: remaining,
	}, nil
}

// parseCounts parses "<subject>:<outcome>:<window>" counter keys.
func parseCounts(in map[string]int64) (map[policy.CounterRequest]int64, error) {
	if len(in) > maxDebugCounts {
		return nil, apperr.InvalidArgument("", "counts must have at most %d entries", maxDebugCounts)
	}
	out := make(map[policy.CounterRequest]int64, len(in)+4)
	for key, v := range in {
		parts := strings.Split(key, ":")
		if len(parts) != 3 {
			return nil, apperr.InvalidArgument("", "counts key %q must be <subject>:<outcome>:<window>", key)
		}
		subject := policy.SubjectKind(parts[0])
		if subject != policy.SubjectIdentity && subject != policy.SubjectAccount && subject != policy.SubjectProxy {
			return nil, apperr.InvalidArgument("", "counts key %q: subject must be identity|account|proxy", key)
		}
		if !policy.ValidOutcome(parts[1]) {
			return nil, apperr.InvalidArgument("", "counts key %q: unknown outcome", key)
		}
		window, err := parseWindow("counts key "+key, parts[2], false)
		if err != nil {
			return nil, err
		}
		if v < 0 {
			return nil, apperr.InvalidArgument("", "counts[%q] must not be negative", key)
		}
		out[policy.CounterRequest{Subject: subject, Outcome: parts[1], Window: window}] = v
	}
	return out, nil
}

// parseBanCounts parses ban history windows.
func parseBanCounts(in map[string]int64) (map[time.Duration]int64, error) {
	if len(in) > maxDebugBanCounts {
		return nil, apperr.InvalidArgument("", "ban_counts must have at most %d entries", maxDebugBanCounts)
	}
	out := make(map[time.Duration]int64, len(in))
	for key, v := range in {
		window, err := parseWindow("ban_counts key "+key, key, false)
		if err != nil {
			return nil, err
		}
		if v < 0 {
			return nil, apperr.InvalidArgument("", "ban_counts[%q] must not be negative", key)
		}
		out[window] = v
	}
	return out, nil
}

// parseWindow parses a non-permanent duration; empty is allowed only when
// allowEmpty is set.
func parseWindow(field, s string, allowEmpty bool) (time.Duration, error) {
	if s == "" && !allowEmpty {
		return 0, apperr.InvalidArgument("", "%s: duration is required", field)
	}
	d, err := durationx.Parse(s)
	if err != nil {
		return 0, apperr.InvalidArgument("", "%s: %v", field, err)
	}
	if d.IsPermanent() || d < 0 {
		return 0, apperr.InvalidArgument("", "%s: must be a finite duration", field)
	}
	return d.Std(), nil
}

// formatDuration renders a planned action duration.
func formatDuration(a policy.PlannedAction) string {
	switch {
	case a.Permanent:
		return durationx.Permanent.String()
	case a.Duration > 0:
		return durationx.Duration(a.Duration).String()
	}
	return ""
}

// draftPolicies compiles the draft (or published YAML) of a signal or action
// policy and substitutes it for the resolved policy of its kind.
func (s *Service) draftPolicies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, id string, sig *policy.CompiledSignal, act *policy.CompiledAction) (*policy.CompiledSignal, *policy.CompiledAction, error) {
	l, err := s.loadVisiblePolicy(ctx, p, id)
	if l.ns != nil && l.ns.ID != ns.ID {
		// Policies of other namespaces are never usable here; do not reveal
		// whether the caller could read them.
		return nil, nil, apperr.NotFound("policy not found")
	}
	if err != nil {
		return nil, nil, err
	}
	kind := policy.Kind(l.row.Kind)
	if kind != policy.KindSignal && kind != policy.KindAction {
		return nil, nil, apperr.InvalidArgument("", "draft_policy_id must reference a signal or action policy (got %s)", kind)
	}
	q := policysvcdb.New(s.pool)
	var yamlText string
	switch {
	case l.row.DraftYaml != nil:
		yamlText = *l.row.DraftYaml
	case l.row.CurrentVersion > 0:
		v, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: l.row.ID, Version: l.row.CurrentVersion})
		if err != nil {
			return nil, nil, fmt.Errorf("load version %d of policy %s: %w", l.row.CurrentVersion, l.row.ID, err)
		}
		yamlText = v.SpecYaml
	default:
		return nil, nil, apperr.FailedPrecondition("", "policy %q has neither a draft nor a published version", l.row.Name)
	}
	spec, err := parseSpec(kind, yamlText)
	if err != nil {
		return nil, nil, err
	}
	chain, err := resolveChain(ctx, q, ns.ID, spec)
	if err != nil {
		return nil, nil, err
	}
	csig, cact, err := compileChain(kind, chain)
	if err != nil {
		return nil, nil, err
	}
	if kind == policy.KindSignal {
		return csig, act, nil
	}
	return sig, cact, nil
}

// ValidatePolicy parses and validates policy YAML and returns its normalized
// form (policy:read anywhere). Extends references are not resolved because
// the request carries no namespace.
func (s *Service) ValidatePolicy(p *authz.Principal, kind policy.Kind, yamlText string) (ValidationResult, error) {
	if err := requirePrincipal(p); err != nil {
		return ValidationResult{}, err
	}
	if err := s.requireReadAnywhere(p); err != nil {
		return ValidationResult{}, err
	}
	if err := requireKind(kind); err != nil {
		return ValidationResult{}, err
	}
	if yamlText == "" || len(yamlText) > MaxYAMLBytes {
		return ValidationResult{}, apperr.InvalidArgument("", "yaml must be 1..%d bytes", MaxYAMLBytes)
	}
	spec, err := policy.ParseYAML(kind, []byte(yamlText))
	if err != nil {
		return ValidationResult{Valid: false, Errors: problems(err)}, nil
	}
	normalized, err := policy.MarshalYAML(spec)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("encode normalized %s policy: %w", kind, err)
	}
	return ValidationResult{Valid: true, Errors: []string{}, NormalizedYAML: string(normalized)}, nil
}

// problems lists validation problems, or the parse error message.
func problems(err error) []string {
	var verr *policy.ValidationError
	if errors.As(err, &verr) && len(verr.Problems) > 0 {
		out := make([]string, 0, len(verr.Problems))
		for _, pr := range verr.Problems {
			out = append(out, pr.String())
		}
		return out
	}
	return []string{err.Error()}
}

// requireReadAnywhere checks that p holds policy:read in at least one
// namespace it can reach.
func (s *Service) requireReadAnywhere(p *authz.Principal) error {
	if p.Kind == authz.KindSystem || (p.Kind == authz.KindUser && p.IsPlatformAdmin) {
		return nil
	}
	var candidates []*catalog.Namespace
	switch p.Kind {
	case authz.KindToken:
		if ns, ok := s.cat.Namespace(p.NamespaceID); ok {
			candidates = append(candidates, ns)
		}
	case authz.KindUser:
		for _, tenantID := range p.TenantIDs() {
			candidates = append(candidates, s.cat.Namespaces(tenantID)...)
		}
	}
	for _, ns := range candidates {
		if accessFor(p, ns, authz.PermPolicyRead).any() {
			return nil
		}
	}
	return p.Require(authz.PermPolicyRead, authz.Resource{})
}
