package auth

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// Validation patterns shared with the API definitions.
var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
	slugPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	siteNamePattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,63}$`)
)

// Field length limits.
const (
	maxUsernameLength    = 64
	maxDisplayNameLength = 128
	maxDescriptionLength = 1024
	maxEmailLength       = 254
	maxLocaleLength      = 35
	maxTokenNameLength   = 64
	maxTokenScopes       = 64
	maxTokenAllowlist    = 256
	maxTokenRateLimitRPS = 1_000_000
	maxBindingSites      = 500
)

var localePattern = regexp.MustCompile(`^([A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*)?$`)

// NormalizeUsername lower-cases and trims a username.
func NormalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateUsername checks the username pattern (3..64 of [a-z0-9._-],
// starting with a letter or digit).
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return apperr.InvalidArgument("", "username must be 3-64 characters of a-z, 0-9, '.', '_' or '-' and start with a letter or digit")
	}
	return nil
}

// ValidateSlug checks tenant and namespace names (2..63 of [a-z0-9-]).
func ValidateSlug(field, name string) error {
	if !slugPattern.MatchString(name) {
		return apperr.InvalidArgument("", "%s must be 2-63 characters of a-z, 0-9 or '-' and start with a letter or digit", field)
	}
	return nil
}

// ValidSiteName reports whether s matches the site name pattern.
func ValidSiteName(s string) bool {
	return siteNamePattern.MatchString(s)
}

// validateText checks a free-text field: valid UTF-8, no control characters
// other than tab/newline, at most max characters.
func validateText(field, s string, maxLen int) error {
	if !utf8.ValidString(s) {
		return apperr.InvalidArgument("", "%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(s) > maxLen {
		return apperr.InvalidArgument("", "%s must be at most %d characters", field, maxLen)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return apperr.InvalidArgument("", "%s must not contain control characters", field)
	}
	return nil
}

// validateName checks a single-line name.
func validateName(field, s string, maxLen int) error {
	if strings.TrimSpace(s) == "" {
		return apperr.InvalidArgument("", "%s is required", field)
	}
	if err := validateText(field, s, maxLen); err != nil {
		return err
	}
	if strings.ContainsAny(s, "\n\t") {
		return apperr.InvalidArgument("", "%s must be a single line", field)
	}
	return nil
}

// validateEmail accepts "" or a bare address.
func validateEmail(email string) error {
	if email == "" {
		return nil
	}
	if len(email) > maxEmailLength {
		return apperr.InvalidArgument("", "email must be at most %d characters", maxEmailLength)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return apperr.InvalidArgument("", "email must be a valid address")
	}
	return nil
}

// validateLocale accepts "" or a BCP 47-like tag.
func validateLocale(locale string) error {
	if len(locale) > maxLocaleLength || !localePattern.MatchString(locale) {
		return apperr.InvalidArgument("", "locale must be a language tag such as \"en\" or \"zh-CN\"")
	}
	return nil
}

// activeTenant returns the principal's active tenant or an invalid_argument
// error when none is selected.
func activeTenant(p *authz.Principal) (string, error) {
	if p == nil {
		return "", apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if p.TenantID == "" {
		return "", apperr.InvalidArgument("", "active tenant is required (%s header)", HeaderTenant)
	}
	return p.TenantID, nil
}

// superuser reports whether p passes every check (system or platform admin user).
func superuser(p *authz.Principal) bool {
	return p != nil && (p.Kind == authz.KindSystem || (p.Kind == authz.KindUser && p.IsPlatformAdmin))
}

// requireUser returns p when it is a console user.
func requireUser(p *authz.Principal) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if p.Kind != authz.KindUser {
		return apperr.PermissionDenied(apperr.ReasonPermissionDenied, "a console user session is required")
	}
	return nil
}

// tenantOwner reports whether p holds a tenant-wide owner binding in tenantID
// (or is a superuser).
func tenantOwner(p *authz.Principal, tenantID string) bool {
	if superuser(p) {
		return true
	}
	if p == nil || p.Kind != authz.KindUser {
		return false
	}
	for _, b := range p.Bindings {
		if b.TenantID == tenantID && b.Role == authz.RoleOwner && b.NamespaceID == "" && len(b.SiteIDs) == 0 {
			return true
		}
	}
	return false
}

// tenantVisible reports whether objects of tenantID are addressable by p: the
// tenant is p's active tenant, or p is a superuser without an active tenant.
func tenantVisible(p *authz.Principal, tenantID string) bool {
	return p.TenantID == tenantID || (superuser(p) && p.TenantID == "")
}
