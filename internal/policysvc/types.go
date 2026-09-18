package policysvc

import (
	"time"

	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// Policy is the administrative view of a policy.
type Policy struct {
	ID             string
	NamespaceID    string
	NamespaceName  string
	Kind           policy.Kind
	Name           string
	Description    string
	CurrentVersion int
	// PublishedYAML is the YAML of the current version; empty in list results.
	PublishedYAML string
	// DraftYAML is the draft; empty in list results (see HasDraft).
	DraftYAML      string
	HasDraft       bool
	DraftUpdatedBy string
	DraftUpdatedAt *time.Time
	// Bindings lists the bindings visible to the caller.
	Bindings  []Binding
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Binding is a policy binding with names resolved from the catalog snapshot.
type Binding struct {
	ID                string
	PolicyID          string
	PolicyName        string
	Kind              policy.Kind
	NamespaceID       string
	NamespaceName     string
	SiteID            string
	SiteName          string
	Client            string
	EndpointGroupID   string
	EndpointGroupName string
	Level             policy.Level
	CreatedBy         string
	CreatedAt         time.Time
}

// Version is a published policy version.
type Version struct {
	Version   int
	SpecYAML  string
	Comment   string
	CreatedBy string
	CreatedAt time.Time
}

// ListPoliciesInput selects a page of policies. AfterKind/AfterName is the
// keyset cursor (the last item of the previous page).
type ListPoliciesInput struct {
	Kind      policy.Kind
	Search    string
	PageSize  int
	AfterKind string
	AfterName string
}

// PolicyPage is a page of policies; More reports whether another page follows.
type PolicyPage struct {
	Policies []Policy
	More     bool
	Total    int
}

// CreateInput creates a policy from YAML.
type CreateInput struct {
	Kind    policy.Kind
	YAML    string
	Publish bool
	Comment string
}

// PublishInput publishes the draft of a policy. ExpectedVersion > 0 enables
// the optimistic concurrency check.
type PublishInput struct {
	ID              string
	Comment         string
	ExpectedVersion int
}

// VersionPage is a page of versions, newest first.
type VersionPage struct {
	Versions []Version
	More     bool
	Total    int
}

// Diff is the comparison of two policy YAML documents.
type Diff struct {
	FromYAML    string
	ToYAML      string
	UnifiedDiff string
}

// Target names a binding or resolution level by site, client and endpoint
// group names: no site = namespace, site = site, site+client = client,
// site+client+endpoint group = endpoint group.
type Target struct {
	Site          string
	Client        string
	EndpointGroup string
}

// ResolvedPolicy is the effective policy of one kind.
type ResolvedPolicy struct {
	Kind     policy.Kind
	PolicyID string
	Name     string
	Version  int
	Level    policy.Level
	// YAML is the effective YAML; extends chains are flattened.
	YAML string
}

// DebugReport holds the facts of a sample report.
type DebugReport struct {
	URI           string
	Method        string
	HTTPStatus    int
	BusinessCode  string
	ErrorKind     string
	Markers       []string
	OutcomeHint   string
	LatencyMs     int64
	ResponseBytes int64
}

// DebugInput describes a simulated report and the assumed hot state after it
// was observed. EndpointGroup or URI selects the endpoint group; when both are
// empty Report.URI is matched against the URI rules.
type DebugInput struct {
	Site          string
	Client        string
	EndpointGroup string
	URI           string
	Report        DebugReport
	IdentityState string
	// Counts is keyed "<subject>:<outcome>:<window>", e.g. "identity:captcha:1h".
	Counts          map[string]int64
	HasAccount      bool
	HasProxy        bool
	EndpointScore   float64
	EndpointSamples int
	GlobalScore     float64
	GlobalSamples   int
	EndpointStreak  int
	SiteStreak      int
	ProxyStreak     int
	// BanCounts is keyed by window, e.g. "30d".
	BanCounts                 map[string]int64
	DraftPolicyID             string
	EndpointCooldownRemaining string
}

// PlannedAction is an action the evaluator would apply.
type PlannedAction struct {
	Action    string
	Scope     string
	Duration  string
	Permanent bool
	Severity  int
	RuleName  string
	Source    string
	RuleIndex int
}

// CounterRequirement is a counter read by the action rules.
type CounterRequirement struct {
	Subject string
	Outcome string
	Window  string
}

// DebugResult is the simulated classification and action plan.
type DebugResult struct {
	EndpointGroupID   string
	EndpointGroupName string
	Outcome           string
	Blame             string
	MatchedRuleIndex  int
	MatchedRuleName   string
	Actions           []PlannedAction
	Counters          []CounterRequirement
	Mode              string
}

// ValidationResult is the result of ValidatePolicy.
type ValidationResult struct {
	Valid          bool
	Errors         []string
	NormalizedYAML string
}
