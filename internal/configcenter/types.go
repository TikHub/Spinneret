package configcenter

import (
	"encoding/json"
	"time"
)

// Content formats.
const (
	FormatJSON = "json"
	FormatYAML = "yaml"
	FormatText = "text"
)

// Reserved runtime group and its items.
const (
	// RuntimeGroup is the reserved, read-only group maintained by the system.
	RuntimeGroup = "_runtime"
	// RuntimeBreakers is the "_runtime" item with endpoint group breaker states.
	RuntimeBreakers = "breakers"
	// RuntimeSiteSwitches is the "_runtime" item with site pause switches.
	RuntimeSiteSwitches = "site_switches"
)

// Limits enforced on requests (mirroring the protovalidate rules so the
// service is safe to call directly).
const (
	MaxContentBytes     = 4 << 20
	MaxSchemaBytes      = 1 << 20
	MaxDescriptionLen   = 1024
	MaxCommentLen       = 1024
	MaxNodeItems        = 200
	MaxSecretRefs       = 100
	maxListGroups       = 10_000
	defaultDiffContext  = 3
	watchRetryAfterMs   = 1000
	maxWatchSpins       = 3
	deadlineSafetyShift = 100 * time.Millisecond
)

// Item is a config item with its draft and current published version.
type Item struct {
	ID            string
	NamespaceID   string
	NamespaceName string
	Group         string
	Key           string
	Format        string
	// Schema is the JSON Schema document; nil when none.
	Schema      json.RawMessage
	Description string
	// CurrentVersion is 0 when the item was never published.
	CurrentVersion int32
	// PublishedContent is the raw content of the current version (secret
	// references unresolved). Empty in list results.
	PublishedContent string
	// DraftContent is nil when there is no draft. Always nil in list results;
	// use HasDraft there.
	DraftContent   *string
	HasDraft       bool
	DraftUpdatedBy string
	DraftUpdatedAt *time.Time
	PublishedBy    string
	PublishedAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// ReadOnly marks the virtual "_runtime" items (they have no ID).
	ReadOnly bool
}

// ListOptions selects a page of config items.
type ListOptions struct {
	Group     string
	Search    string
	PageSize  int
	PageToken string
}

// ItemPage is a page of config items.
type ItemPage struct {
	Items         []Item
	NextPageToken string
	Total         int
}

// CreateRequest creates a config item.
type CreateRequest struct {
	Group       string
	Key         string
	Format      string
	SchemaJSON  string
	Description string
	Content     string
	Publish     bool
	Comment     string
}

// DraftRequest saves draft content; nil optional fields are left unchanged.
type DraftRequest struct {
	ID          string
	Content     string
	SchemaJSON  *string
	Description *string
}

// PublishRequest publishes the draft of an item.
type PublishRequest struct {
	ID              string
	Comment         string
	ExpectedVersion int32
}

// RollbackRequest republishes an old version.
type RollbackRequest struct {
	ID      string
	Version int32
	Comment string
}

// Version is an immutable published version.
type Version struct {
	Version       int32
	Content       string
	Comment       string
	SourceVersion int32
	PublishedBy   string
	PublishedAt   time.Time
}

// VersionPage is a page of versions, newest first.
type VersionPage struct {
	Versions      []Version
	NextPageToken string
	Total         int
}

// Diff is the comparison of two versions (or a version and the draft).
type Diff struct {
	FromContent string
	ToContent   string
	Unified     string
}

// Ref addresses an item inside a namespace.
type Ref struct {
	Group string
	Key   string
}

// WatchRef is a watched item with the version the caller holds.
type WatchRef struct {
	Group   string
	Key     string
	Version int32
}

// NodeItem is a published item as delivered to nodes (secret references
// resolved).
type NodeItem struct {
	Namespace     string
	Group         string
	Key           string
	Format        string
	Version       int32
	Content       string
	UpdatedAt     *time.Time
	HasSecretRefs bool
}
