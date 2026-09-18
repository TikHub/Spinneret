package identitysvc

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// Attribute limits.
const (
	MaxTags            = 32
	MaxLabels          = 32
	MaxLabelValueBytes = 256
	MaxRegionBytes     = 64
	MaxAccountRefBytes = 256
	MaxNotesBytes      = 4096
	MaxReasonBytes     = 512
	MaxPageTokenBytes  = 1024
	DefaultPageSize    = 50
	MaxPageSize        = 500
)

var (
	tagPattern      = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)
	labelKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
	actionPattern   = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
)

// Manual operations.
var (
	identityOperations = []string{
		"cooldown", "ban", "unban", "quarantine", "unquarantine", "expire",
		"disable", "enable", "archive", "restore", "activate", "reset_stats",
	}
	accountOperations = []string{"ban", "unban", "cooldown", "disable", "enable"}
	operationScopes   = []string{"", "identity_endpoint", "identity_site", "identity"}
	revertActions     = []string{"ban", "quarantine", "expire", "cooldown"}
	identityStates    = []string{
		StatePending, StateActive, StateExpired, StateBanned, StateQuarantined, StateDisabled, StateRetired,
	}
	accountStates = []string{"", AccountActive, AccountBanned, AccountDisabled}
	subjectKinds  = []string{"", "identity", "account", "proxy", "endpoint_group", "site"}
)

func invalid(format string, args ...any) *apperr.Error {
	return apperr.InvalidArgument(apperr.ReasonInvalidArgument, format, args...)
}

// validateTags checks tags and returns them deduplicated (nil stays nil).
func validateTags(tags []string) ([]string, error) {
	if tags == nil {
		return nil, nil
	}
	out := dedupe(tags)
	if len(out) > MaxTags {
		return nil, invalid("at most %d tags are allowed, got %d", MaxTags, len(out))
	}
	for _, t := range out {
		if !tagPattern.MatchString(t) {
			return nil, invalid("tag %q must match %s", truncateText(t, 64), tagPattern)
		}
	}
	return out, nil
}

// validateLabels checks label keys and values.
func validateLabels(labels map[string]string) error {
	if len(labels) > MaxLabels {
		return invalid("at most %d labels are allowed, got %d", MaxLabels, len(labels))
	}
	for k, v := range labels {
		if !labelKeyPattern.MatchString(k) {
			return invalid("label key %q must match %s", truncateText(k, 64), labelKeyPattern)
		}
		if len(v) > MaxLabelValueBytes || !utf8.ValidString(v) {
			return invalid("label %q: value must be valid UTF-8 of at most %d bytes", k, MaxLabelValueBytes)
		}
	}
	return nil
}

// validateText checks a free-form string attribute.
func validateText(field, v string, maxBytes int) error {
	if len(v) > maxBytes {
		return invalid("%s must be at most %d bytes", field, maxBytes)
	}
	if !utf8.ValidString(v) {
		return invalid("%s must be valid UTF-8", field)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return invalid("%s must not contain control characters", field)
		}
	}
	return nil
}

// validateAccountRef checks an account external reference ("" is allowed).
func validateAccountRef(ref string) error {
	return validateText("account reference", ref, MaxAccountRefBytes)
}

// validateOperation checks a manual operation against the allowed operations.
func validateOperation(req OperationRequest, allowed []string, withScope bool) error {
	if !slices.Contains(allowed, req.Operation) {
		return invalid("operation %q must be one of %s", truncateText(req.Operation, 32), strings.Join(allowed, ", "))
	}
	if withScope {
		if !slices.Contains(operationScopes, req.Scope) {
			return invalid("scope %q must be one of identity_endpoint, identity_site, identity", truncateText(req.Scope, 32))
		}
		if req.Scope == "identity_endpoint" && req.EndpointGroupID == "" {
			return invalid("endpoint_group_id is required for scope identity_endpoint")
		}
	} else if req.Scope != "" || req.EndpointGroupID != "" {
		return invalid("scope and endpoint_group_id are not supported for this operation")
	}
	if len(req.EndpointGroupID) > 64 {
		return invalid("endpoint_group_id must be at most 64 bytes")
	}
	switch req.Operation {
	case "cooldown":
		if req.Duration.IsPermanent() || req.Duration <= 0 {
			return invalid("duration is required for cooldown and must be greater than zero (permanent is not allowed)")
		}
	case "ban":
		if !req.Duration.IsPermanent() && req.Duration <= 0 {
			return invalid("duration is required for ban and must be greater than zero or permanent")
		}
	case "quarantine":
		if req.Duration.IsPermanent() || req.Duration < 0 {
			return invalid("duration \"permanent\" is only allowed for ban")
		}
	}
	return validateText("reason", req.Reason, MaxReasonBytes)
}

// pageSize normalizes a requested page size.
func pageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return n
	}
}

// encodeCursor serializes an opaque pagination cursor.
func encodeCursor(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Cursor values are plain structs of strings, numbers and times.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses a cursor produced by encodeCursor. An empty token
// returns false.
func decodeCursor(token string, v any) (bool, error) {
	if token == "" {
		return false, nil
	}
	if len(token) > MaxPageTokenBytes {
		return false, invalid("invalid page_token")
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, v) != nil {
		return false, invalid("invalid page_token")
	}
	return true, nil
}

// truncateText shortens user-supplied identifiers (never secret values) for
// error messages without splitting UTF-8 sequences.
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
