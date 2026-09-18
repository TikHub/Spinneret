package identitysvc

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identity"
)

// Identity lifecycle states (spec §4).
const (
	StatePending     = "pending"
	StateActive      = "active"
	StateExpired     = "expired"
	StateBanned      = "banned"
	StateQuarantined = "quarantined"
	StateDisabled    = "disabled"
	StateRetired     = "retired"
)

// Account states.
const (
	AccountActive   = "active"
	AccountBanned   = "banned"
	AccountDisabled = "disabled"
)

// ActionPayloadUpdate is the state event action recorded when a payload
// update (import or UpdateIdentityPayload) changes the lifecycle state.
const ActionPayloadUpdate = "payload_update"

var (
	errNoCatalog  = errors.New("identitysvc: catalog is not configured")
	errNoPool     = errors.New("identitysvc: database pool is not configured")
	errNoCipher   = errors.New("identitysvc: cipher is not configured")
	errPepper     = errors.New("identitysvc: dedupe pepper is missing or shorter than 16 bytes")
	errNoOperator = errors.New("identitysvc: operator is not configured")
)

// IdentityType is an identity type as returned by the service.
type IdentityType struct {
	ID            string
	NamespaceName string
	SiteID        string
	SiteName      string
	Client        string
	Name          string
	Description   string
	SpecYAML      string
	// JSONSchema is the payload JSON Schema (canonical JSON).
	JSONSchema    json.RawMessage
	Version       int
	IdentityCount int
	// Spec is the effective spec (defaults applied); nil when the stored spec
	// cannot be decoded.
	Spec      *identity.TypeSpec
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TypeQuery selects identity types.
type TypeQuery struct {
	Site      string
	Client    string
	Search    string
	PageSize  int
	PageToken string
}

// TypePage is a page of identity types.
type TypePage struct {
	Types         []IdentityType
	NextPageToken string
	Total         int
}

// PreviewInput selects the type and sample payload of a delivery preview.
// Exactly one of TypeID and SpecYAML must be set.
type PreviewInput struct {
	Site        string
	TypeID      string
	SpecYAML    string
	PayloadJSON string
}

// PreviewResult is a rendered delivery preview. Credential is nil when Errors
// is not empty; Normalized is set once the payload normalized successfully.
type PreviewResult struct {
	Credential *identity.Credential
	Errors     []string
	Normalized map[string]any
}

// Identity is an identity as returned by the service.
type Identity struct {
	ID              string
	NamespaceName   string
	SiteID          string
	SiteName        string
	Client          string
	TypeID          string
	TypeName        string
	AccountID       string
	AccountRef      string
	State           string
	StateReason     string
	StateChangedAt  time.Time
	BanUntil        *time.Time
	QuarantineUntil *time.Time
	Region          string
	Tags            []string
	Labels          map[string]string
	PayloadVersion  int
	ActivatedAt     *time.Time
	LastUsedAt      *time.Time
	// GlobalScore and GlobalSamples come from the last hot-state snapshot
	// (DefaultBaselineScore and 0 when there is none).
	GlobalScore   float64
	GlobalSamples int
	BoundProxyID  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// IdentityDetail is an identity with its payload and recent state events.
type IdentityDetail struct {
	Identity  Identity
	Namespace *catalog.Namespace
	Site      *catalog.Site
	// Payload is the current payload, masked unless Revealed. It is nil when
	// the payload could not be loaded (logged).
	Payload      map[string]any
	Revealed     bool
	RecentEvents []StateEvent
}

// IdentityRef locates an identity in the catalog.
type IdentityRef struct {
	ID        string
	Namespace *catalog.Namespace
	Site      *catalog.Site
}

// IdentityFilter selects identities; conditions are combined with AND.
type IdentityFilter struct {
	Site           string
	Type           string
	States         []string
	Tags           []string
	AccountRef     string
	Region         string
	Search         string
	MinScore       *float64
	MaxScore       *float64
	IncludeRetired bool
}

// Order keys of ListIdentities.
const (
	OrderCreatedAt      = "created_at"
	OrderUpdatedAt      = "updated_at"
	OrderStateChangedAt = "state_changed_at"
	OrderLastUsedAt     = "last_used_at"
	OrderScore          = "score"
)

// IdentityQuery selects a page of identities.
type IdentityQuery struct {
	Filter     IdentityFilter
	PageSize   int
	PageToken  string
	OrderBy    string
	Descending bool
}

// IdentityPage is a page of identities.
type IdentityPage struct {
	Identities    []Identity
	NextPageToken string
	// Total is the number of matching identities, capped at MaxCountedRows.
	Total int
}

// ImportInput describes an identity import.
type ImportInput struct {
	Site   string
	Type   string
	Format string
	Data   string
	// Mode is "", "upsert" or "create_only".
	Mode   string
	DryRun bool
}

// ImportFailure is one rejected import row.
type ImportFailure struct {
	Line    int
	Message string
}

// ImportResult summarizes an import.
type ImportResult struct {
	Created   int
	Updated   int
	Unchanged int
	Failed    []ImportFailure
}

// IdentityUpdate changes identity attributes; nil / false fields are left
// unchanged.
type IdentityUpdate struct {
	Region     *string
	Tags       []string
	SetTags    bool
	Labels     map[string]string
	SetLabels  bool
	AccountRef *string
}

// Account is an account as returned by the service.
type Account struct {
	ID            string
	SiteID        string
	SiteName      string
	ExternalRef   string
	Region        string
	Tags          []string
	State         string
	BanUntil      *time.Time
	CooldownUntil *time.Time
	Notes         string
	IdentityCount int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AccountQuery selects a page of accounts.
type AccountQuery struct {
	Site      string
	Search    string
	State     string
	PageSize  int
	PageToken string
}

// AccountPage is a page of accounts.
type AccountPage struct {
	Accounts      []Account
	NextPageToken string
	Total         int
}

// AccountUpsert creates or updates an account by site and external reference.
type AccountUpsert struct {
	Site        string
	ExternalRef string
	Region      string
	Tags        []string
	Notes       string
}

// RevertInput selects the actions RevertActions reverts. Site is a site name
// ("" = every accessible site); Request.SiteID is filled by the service.
type RevertInput struct {
	Site    string
	Request RevertRequest
}

// StateEvent is a lifecycle or disposition event.
type StateEvent struct {
	ID                string
	CreatedAt         time.Time
	NamespaceName     string
	SiteID            string
	SiteName          string
	SubjectKind       string
	SubjectID         string
	EndpointGroupID   string
	EndpointGroupName string
	FromState         string
	ToState           string
	Action            string
	Scope             string
	Until             *time.Time
	Permanent         bool
	Outcome           string
	PolicyID          string
	PolicyVersion     int
	Rule              string
	ReportID          string
	LeaseID           string
	Actor             string
	Reason            string
	Shadow            bool
}

// StateEventQuery selects state events, newest first.
type StateEventQuery struct {
	SubjectKind string
	SubjectID   string
	Site        string
	Actions     []string
	Shadow      *bool
	From, To    time.Time
	PageSize    int
	PageToken   string
}

// StateEventPage is a page of state events.
type StateEventPage struct {
	Events        []StateEvent
	NextPageToken string
}
