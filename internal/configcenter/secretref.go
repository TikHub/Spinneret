package configcenter

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// Secret reference syntax: ${secret:<path>} or ${secret:<path>#<version>}.
const (
	secretRefPrefix = "${secret:"
	maxSecretPath   = 256
	maxVersionDigit = 10
	// maxSecretRefLen bounds the search for the closing brace.
	maxSecretRefLen = len(secretRefPrefix) + maxSecretPath + 1 + maxVersionDigit + 1
)

var secretPathPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_./-]{0,255}$`)

// SecretRef is a secret reference found in config content. Version 0 means
// the current version of the secret.
type SecretRef struct {
	Path    string
	Version int
}

// String renders the reference as "<path>" or "<path>#<version>".
func (r SecretRef) String() string {
	if r.Version > 0 {
		return r.Path + "#" + strconv.Itoa(r.Version)
	}
	return r.Path
}

// ValidSecretPath reports whether p is a canonical namespace-relative secret
// path: ^[a-z0-9][a-z0-9_./-]{0,255}$ without empty, "." or ".." segments.
func ValidSecretPath(p string) bool {
	if !secretPathPattern.MatchString(p) {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// secretRefSpan is one reference occurrence in content.
type secretRefSpan struct {
	start, end int // content[start:end] is the whole "${secret:...}" token
	ref        SecretRef
}

// scanSecretRefs finds every reference occurrence. Every "${secret:" token
// must form a valid reference; there is no escape syntax.
func scanSecretRefs(content string) ([]secretRefSpan, error) {
	var spans []secretRefSpan
	offset := 0
	for {
		i := strings.Index(content[offset:], secretRefPrefix)
		if i < 0 {
			return spans, nil
		}
		start := offset + i
		bodyStart := start + len(secretRefPrefix)
		window := content[bodyStart:min(len(content), start+maxSecretRefLen)]
		closing := strings.IndexByte(window, '}')
		if closing < 0 {
			return nil, apperr.InvalidArgument("", "unterminated or too long secret reference at byte %d", start)
		}
		ref, err := parseSecretRefBody(window[:closing])
		if err != nil {
			return nil, apperr.InvalidArgument("", "invalid secret reference at byte %d: %v", start, err)
		}
		end := bodyStart + closing + 1
		spans = append(spans, secretRefSpan{start: start, end: end, ref: ref})
		offset = end
	}
}

type refError string

func (e refError) Error() string { return string(e) }

func parseSecretRefBody(body string) (SecretRef, error) {
	path, version, hasVersion := strings.Cut(body, "#")
	if !ValidSecretPath(path) {
		return SecretRef{}, refError(`path must match ^[a-z0-9][a-z0-9_./-]{0,255}$ without empty, "." or ".." segments`)
	}
	ref := SecretRef{Path: path}
	if !hasVersion {
		return ref, nil
	}
	if version == "" || len(version) > maxVersionDigit || version[0] == '0' || strings.Trim(version, "0123456789") != "" {
		return SecretRef{}, refError("version must be a positive integer")
	}
	v, err := strconv.ParseInt(version, 10, 32)
	if err != nil {
		return SecretRef{}, refError("version is out of range")
	}
	ref.Version = int(v)
	return ref, nil
}

// ParseSecretRefs returns the distinct secret references of content in order
// of first appearance. Malformed references and more than MaxSecretRefs
// distinct references are invalid_argument errors.
func ParseSecretRefs(content string) ([]SecretRef, error) {
	spans, err := scanSecretRefs(content)
	if err != nil {
		return nil, err
	}
	return distinctRefs(spans)
}

func distinctRefs(spans []secretRefSpan) ([]SecretRef, error) {
	if len(spans) == 0 {
		return nil, nil
	}
	seen := make(map[SecretRef]struct{}, len(spans))
	var out []SecretRef
	for _, sp := range spans {
		if _, ok := seen[sp.ref]; ok {
			continue
		}
		if len(out) == MaxSecretRefs {
			return nil, apperr.InvalidArgument("", "content references more than %d distinct secrets", MaxSecretRefs)
		}
		seen[sp.ref] = struct{}{}
		out = append(out, sp.ref)
	}
	return out, nil
}

// substituteSecretRefs replaces every reference span (as returned by
// scanSecretRefs for content) with its value from values. For json content
// the value is JSON-string escaped (references can only occur inside JSON
// strings of valid JSON); yaml and text get the raw value.
func substituteSecretRefs(format, content string, spans []secretRefSpan, values map[SecretRef]string) (string, error) {
	if len(spans) == 0 {
		return content, nil
	}
	var sb strings.Builder
	sb.Grow(len(content))
	last := 0
	for _, sp := range spans {
		v, ok := values[sp.ref]
		if !ok {
			return "", apperr.Internal(refError("unresolved secret reference " + sp.ref.String()))
		}
		sb.WriteString(content[last:sp.start])
		if format == FormatJSON {
			v = jsonStringBody(v)
		}
		sb.WriteString(v)
		last = sp.end
	}
	sb.WriteString(content[last:])
	return sb.String(), nil
}

// jsonStringBody returns s encoded as the inside of a JSON string literal.
func jsonStringBody(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return s
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return string(b[1 : len(b)-1])
}
