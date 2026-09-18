package notify

import (
	"slices"
	"time"
)

// Alert kinds (the event types a channel can subscribe to).
const (
	KindBreakerOpened        = "breaker_opened"
	KindBreakerReopened      = "breaker_reopened"
	KindBreakerClosed        = "breaker_closed"
	KindIdentityLowWatermark = "identity_low_watermark"
	KindProxyLowWatermark    = "proxy_low_watermark"
	KindBanSpike             = "ban_spike"
	KindReportBacklog        = "report_backlog"
	KindUnknownRatioHigh     = "unknown_ratio_high"
	KindClientErrorSpike     = "client_error_spike"
	KindIdentityExpired      = "identity_expired"
	KindSecretExpiring       = "secret_expiring"
	KindTest                 = "test"
)

var alertKinds = []string{
	KindBreakerOpened, KindBreakerReopened, KindBreakerClosed,
	KindIdentityLowWatermark, KindProxyLowWatermark, KindBanSpike, KindReportBacklog,
	KindUnknownRatioHigh, KindClientErrorSpike, KindIdentityExpired, KindSecretExpiring, KindTest,
}

// Kinds returns every alert kind.
func Kinds() []string { return slices.Clone(alertKinds) }

// ValidKind reports whether k is a known alert kind.
func ValidKind(k string) bool { return slices.Contains(alertKinds, k) }

// Severities, from least to most severe.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// ValidSeverity reports whether s is a known severity.
func ValidSeverity(s string) bool { return severityRank(s) > 0 }

// severityRank orders severities; unknown values rank 0.
func severityRank(s string) int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityCritical:
		return 3
	default:
		return 0
	}
}

// Channel kinds.
const (
	ChannelWebhook  = "webhook"
	ChannelFeishu   = "feishu"
	ChannelDingTalk = "dingtalk"
	ChannelWeCom    = "wecom"
	ChannelTelegram = "telegram"
)

var channelKinds = []string{ChannelWebhook, ChannelFeishu, ChannelDingTalk, ChannelWeCom, ChannelTelegram}

// ValidChannelKind reports whether k is a supported channel kind.
func ValidChannelKind(k string) bool { return slices.Contains(channelKinds, k) }

// Limits applied to alerts and channels.
const (
	// DefaultDedupTTL is the de-duplication window used when Alert.DedupTTL is zero.
	DefaultDedupTTL = 10 * time.Minute
	// MaxTitleBytes and MaxMessageBytes bound stored alert texts (longer values are truncated).
	MaxTitleBytes   = 256
	MaxMessageBytes = 4096
	// MaxDetailsBytes bounds the JSON encoding of Alert.Details.
	MaxDetailsBytes = 64 << 10
	// MaxChannelNameLen bounds channel names (characters).
	MaxChannelNameLen = 64
	// MaxChannelSites bounds the site restriction list of a channel.
	MaxChannelSites = 500
	// maxDedupKeyBytes bounds the caller supplied part of a de-duplication key.
	maxDedupKeyBytes = 512
)

// Alert is an alert to emit.
type Alert struct {
	Kind     string
	Severity string
	// TenantID is required; NamespaceID and SiteID are optional ("" = tenant
	// or namespace level).
	TenantID    string
	NamespaceID string
	SiteID      string
	Title       string
	Message     string
	// Details holds kind-specific JSON-encodable values.
	Details map[string]any
	// DedupKey suppresses identical alerts (same tenant, kind and key) for
	// DedupTTL (DefaultDedupTTL when zero). Empty disables de-duplication.
	DedupKey string
	DedupTTL time.Duration
}

// Delivery is the result of delivering an alert through one channel.
type Delivery struct {
	ChannelID   string    `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
	Attempts    int       `json:"attempts"`
	At          time.Time `json:"at"`
}

// AlertEvent is a stored alert.
type AlertEvent struct {
	ID          string
	CreatedAt   time.Time
	TenantID    string
	NamespaceID string
	SiteID      string
	Kind        string
	Severity    string
	Title       string
	Message     string
	Details     map[string]any
	DedupKey    string
	Deliveries  []Delivery
}

// Channel is a notification channel as exposed to callers. Config is always
// masked: secrets, bot tokens, credential-like headers and webhook URLs are
// shown as "••••" followed by their last four characters.
type Channel struct {
	ID                 string
	TenantID           string
	NamespaceID        string // "" for tenant-wide channels
	Name               string
	Kind               string
	Config             map[string]any
	EventTypes         []string
	SiteIDs            []string
	MinSeverity        string
	Enabled            bool
	LastDeliveryAt     *time.Time
	LastDeliveryStatus string
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ChannelInput describes a new channel.
type ChannelInput struct {
	TenantID    string
	NamespaceID string // "" creates a tenant-wide channel
	Name        string
	Kind        string
	Config      map[string]any
	EventTypes  []string
	SiteIDs     []string // requires NamespaceID
	MinSeverity string   // "" = warning
	Enabled     bool
}

// ChannelUpdate replaces the mutable fields of a channel.
type ChannelUpdate struct {
	Name string
	// Config replaces the settings; nil keeps the stored settings. Masked
	// values equal to the masked stored value keep the stored secret.
	Config      map[string]any
	EventTypes  []string
	SiteIDs     []string
	MinSeverity string // "" = warning
	Enabled     bool
}

// ChannelQuery selects a page of channels of one tenant.
type ChannelQuery struct {
	TenantID string
	// IncludeTenant includes tenant-wide channels.
	IncludeTenant bool
	// NamespaceIDs includes the channels of these namespaces.
	NamespaceIDs []string
	// AfterName continues after the channel with this name ("" = first page).
	AfterName string
	// Limit is the page size (1..500; out-of-range values are clamped).
	Limit int
}

// ChannelPage is one page of channels ordered by name.
type ChannelPage struct {
	Channels []Channel
	// More is true when further channels follow the last one.
	More  bool
	Total int
}

// AlertQuery selects a page of alert events of one tenant, newest first.
type AlertQuery struct {
	TenantID string
	// Visibility: tenant-level alerts (IncludeTenant), alerts of NamespaceIDs
	// and alerts of SiteIDs.
	IncludeTenant bool
	NamespaceIDs  []string
	SiteIDs       []string
	// Optional filters ("" / nil = any).
	NamespaceID string
	SiteID      string
	Kind        string
	Severity    string
	From, To    *time.Time
	// Keyset cursor: continue after (AfterAt, AfterID).
	AfterAt *time.Time
	AfterID string
	// Limit is the page size (1..500; out-of-range values are clamped).
	Limit int
}

// AlertPage is one page of alert events.
type AlertPage struct {
	Events []AlertEvent
	More   bool
}
