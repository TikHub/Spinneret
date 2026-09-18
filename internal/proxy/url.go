package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// Proxy URL schemes.
const (
	SchemeHTTP   = "http"
	SchemeHTTPS  = "https"
	SchemeSOCKS5 = "socks5"
)

// URL limits.
const (
	// MaxURLLength bounds a proxy URL including credentials.
	MaxURLLength = 2048
	// maxCredentialLength bounds the decoded user name and password.
	maxCredentialLength = 255
	// maxHostLength bounds a DNS host name (RFC 1035).
	maxHostLength = 253
	// maxLabelLength bounds one DNS label.
	maxLabelLength = 63
	// usernameHintChars is the number of user name characters kept in username_hint.
	usernameHintChars = 4
)

// urlField is the vault AAD field of the sealed proxy URL.
const urlField = "url"

// errURLInvalid is the generic parse failure. Parse errors of net/url embed
// the raw input, which may contain credentials, so they are never surfaced.
var errURLInvalid = errors.New("invalid proxy url")

// ParsedURL is a validated, normalized proxy URL.
type ParsedURL struct {
	// Scheme is http, https or socks5 (lower case).
	Scheme string
	// Host is a lower-case DNS name or a canonical IP address (IPv6 without brackets).
	Host string
	// Port is 1..65535.
	Port int
	// Username and Password are the decoded credentials; HasUser reports
	// whether the URL carries a user name.
	Username string
	Password string
	HasUser  bool
}

// ParseProxyURL parses and validates a proxy URL of the form
// scheme://[user[:password]@]host:port with scheme http, https or socks5 and an
// explicit port. Credentials are percent-decoded. Error messages never contain
// the input.
func ParseProxyURL(raw string) (ParsedURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ParsedURL{}, errors.New("proxy url is empty")
	}
	if len(raw) > MaxURLLength {
		return ParsedURL{}, errors.New("proxy url is too long")
	}
	if !strings.Contains(raw, "://") {
		return ParsedURL{}, errors.New("proxy url must start with http://, https:// or socks5://")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ParsedURL{}, parseErrorHint(err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case SchemeHTTP, SchemeHTTPS, SchemeSOCKS5:
	default:
		return ParsedURL{}, errors.New("proxy url scheme must be http, https or socks5")
	}
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return ParsedURL{}, errors.New("proxy url must not contain a path, query or fragment")
	}
	out := ParsedURL{Scheme: scheme}
	if out.Host, err = normalizeHost(u.Host); err != nil {
		return ParsedURL{}, err
	}
	portStr := u.Port()
	if portStr == "" {
		return ParsedURL{}, errors.New("proxy url must include a port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return ParsedURL{}, errors.New("proxy url port must be between 1 and 65535")
	}
	out.Port = port
	if u.User != nil {
		out.Username = u.User.Username()
		out.Password, _ = u.User.Password()
		out.HasUser = true
		if err := validateCredentials(out.Username, out.Password); err != nil {
			return ParsedURL{}, err
		}
	}
	return out, nil
}

// parseErrorHint maps a net/url error to a message without the input.
func parseErrorHint(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "invalid port"):
		return errors.New("proxy url port must be between 1 and 65535")
	case strings.Contains(msg, "invalid URL escape"):
		return errors.New("proxy url contains an invalid percent-encoding")
	case strings.Contains(msg, "invalid character") && strings.Contains(msg, "host"):
		return errors.New("proxy url host is invalid")
	default:
		return errURLInvalid
	}
}

// normalizeHost validates the host part (without port) of a URL authority.
func normalizeHost(authority string) (string, error) {
	host := authority
	if h, _, err := net.SplitHostPort(authority); err == nil {
		host = h
	} else if strings.HasPrefix(authority, "[") {
		host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
	}
	if host == "" {
		return "", errors.New("proxy url host is empty")
	}
	if strings.Contains(host, ":") || strings.HasPrefix(authority, "[") {
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || addr.Zone() != "" || !strings.HasPrefix(authority, "[") {
			return "", errors.New("proxy url host is not a valid IPv6 address")
		}
		if addr.Is4In6() {
			return addr.Unmap().String(), nil
		}
		return addr.String(), nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String(), nil
	}
	host = strings.ToLower(host)
	if err := validateHostname(host); err != nil {
		return "", err
	}
	return host, nil
}

// validateHostname checks a lower-case DNS host name. Underscores are accepted
// because some providers use them in gateway names.
func validateHostname(host string) error {
	if len(host) > maxHostLength {
		return errors.New("proxy url host is too long")
	}
	allNumeric := true
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > maxLabelLength {
			return errors.New("proxy url host is invalid")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("proxy url host is invalid")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c == '-', c == '_':
				allNumeric = false
			default:
				return errors.New("proxy url host is invalid")
			}
		}
	}
	if allNumeric {
		return errors.New("proxy url host is not a valid IPv4 address")
	}
	return nil
}

func validateCredentials(user, pass string) error {
	if user == "" {
		return errors.New("proxy url user name must not be empty when credentials are given")
	}
	if len(user) > maxCredentialLength || len(pass) > maxCredentialLength {
		return errors.New("proxy url credentials are too long")
	}
	for _, s := range []string{user, pass} {
		if !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return errors.New("proxy url credentials contain invalid characters")
		}
	}
	return nil
}

// HostPort returns "host:port" with IPv6 hosts in brackets.
func (u ParsedURL) HostPort() string {
	return net.JoinHostPort(u.Host, strconv.Itoa(u.Port))
}

// userinfo returns the URL user info or nil without credentials. An empty
// password is normalized to no password.
func (u ParsedURL) userinfo() *url.Userinfo {
	if !u.HasUser {
		return nil
	}
	if u.Password == "" {
		return url.User(u.Username)
	}
	return url.UserPassword(u.Username, u.Password)
}

// String returns the normalized URL including percent-encoded credentials.
// It is secret: never log it.
func (u ParsedURL) String() string {
	return (&url.URL{Scheme: u.Scheme, User: u.userinfo(), Host: u.HostPort()}).String()
}

// URL returns the normalized URL as *url.URL (including credentials).
func (u ParsedURL) URL() *url.URL {
	return &url.URL{Scheme: u.Scheme, User: u.userinfo(), Host: u.HostPort()}
}

// DisplayURL returns the URL without credentials.
func (u ParsedURL) DisplayURL() string {
	return (&url.URL{Scheme: u.Scheme, Host: u.HostPort()}).String()
}

// UsernameHint returns the first four characters of the user name followed by
// "***", or "" without credentials.
func (u ParsedURL) UsernameHint() string {
	if !u.HasUser {
		return ""
	}
	runes := []rune(u.Username)
	if len(runes) > usernameHintChars {
		runes = runes[:usernameHintChars]
	}
	return string(runes) + "***"
}

// URLHash returns HMAC-SHA256(pepper, normalized URL), the per-namespace
// de-duplication key.
func URLHash(pepper []byte, u ParsedURL) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(u.String()))
	return mac.Sum(nil)
}

// SealURL encrypts the normalized URL of proxy proxyID.
func SealURL(c *vault.Cipher, proxyID string, u ParsedURL) (vault.Sealed, error) {
	return c.Seal([]byte(u.String()), vault.AAD(proxyID, urlField))
}

// OpenURL decrypts and parses the sealed URL of proxy proxyID.
func OpenURL(c *vault.Cipher, proxyID string, s vault.Sealed) (ParsedURL, error) {
	plain, err := c.Open(s, vault.AAD(proxyID, urlField))
	if err != nil {
		return ParsedURL{}, err
	}
	u, err := ParseProxyURL(string(plain))
	clear(plain)
	if err != nil {
		return ParsedURL{}, errors.New("stored proxy url is invalid")
	}
	return u, nil
}
