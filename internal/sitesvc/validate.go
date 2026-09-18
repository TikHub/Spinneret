package sitesvc

import (
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

var (
	// namePattern applies to site and endpoint group names.
	namePattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,63}$`)
	// clientPattern applies to client types.
	clientPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
)

func validateName(field, name string) error {
	if !namePattern.MatchString(name) {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
			"%s must match %s", field, namePattern.String())
	}
	return nil
}

func validateText(field, value string, maxLen int) error {
	if !utf8.ValidString(value) {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxLen {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "%s must be at most %d characters", field, maxLen)
	}
	return nil
}

func validateOptionalText(field string, value *string, maxLen int) error {
	if value == nil {
		return nil
	}
	return validateText(field, *value, maxLen)
}

// validateClients checks a complete client list: 1..MaxClients unique client
// names matching clientPattern.
func validateClients(clients []string) error {
	if len(clients) == 0 {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at least one client is required")
	}
	if len(clients) > MaxClients {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d clients are allowed", MaxClients)
	}
	for i, c := range clients {
		if !clientPattern.MatchString(c) {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				"clients[%d] must match %s", i, clientPattern.String())
		}
		if slices.Contains(clients[:i], c) {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "client %q is listed twice", c)
		}
	}
	return nil
}

func validateLowWatermark(v int) error {
	if v < 0 || v > int(^uint32(0)>>1) {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "low_watermark must be between 0 and 2147483647")
	}
	return nil
}

// diffClients returns the clients present in next but not in cur (added) and
// in cur but not in next (removed), preserving order.
func diffClients(cur, next []string) (added, removed []string) {
	for _, c := range next {
		if !slices.Contains(cur, c) {
			added = append(added, c)
		}
	}
	for _, c := range cur {
		if !slices.Contains(next, c) {
			removed = append(removed, c)
		}
	}
	return added, removed
}

func clampPageSize(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return n
	}
}
