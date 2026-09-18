// Package apiutil contains helpers shared by the Connect handlers in the
// internal/api/* packages: principal and namespace resolution, pagination
// cursors, protobuf well-known type conversions and duration parsing.
package apiutil

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
)

// Pagination limits shared by list endpoints.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
	// MaxCursorLength bounds the length of a page token accepted by
	// DecodeCursor, so that clients cannot make the server decode arbitrarily
	// large payloads.
	MaxCursorLength = 1024
)

// PageSize normalizes a requested page size.
func PageSize(n int32) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return int(n)
	}
}

// EncodeCursor serializes an opaque pagination cursor.
func EncodeCursor(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeCursor parses a cursor produced by EncodeCursor into v (a pointer).
// An empty token leaves v untouched and returns false. Tokens longer than
// MaxCursorLength bytes, that are not unpadded base64url or whose payload does
// not decode into v are rejected with an InvalidArgument error; v may then be
// partially written and must not be used.
func DecodeCursor(token string, v any) (bool, error) {
	if token == "" {
		return false, nil
	}
	if len(token) > MaxCursorLength {
		return false, invalidPageToken()
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, v) != nil {
		return false, invalidPageToken()
	}
	return true, nil
}

func invalidPageToken() error {
	return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid page_token")
}

// Principal returns the authenticated principal or an unauthenticated error.
func Principal(ctx context.Context) (*authz.Principal, error) {
	return authz.MustPrincipal(ctx)
}

// Namespace resolves a namespace by name for the calling principal. Tokens
// always resolve to their own namespace (a non-empty name must match it).
// Users resolve inside their active tenant (principal.TenantID).
func Namespace(ctx context.Context, cat catalog.Catalog, name string) (*authz.Principal, *catalog.Namespace, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, nil, err
	}
	if p.Kind == authz.KindToken {
		if name != "" && name != p.NamespaceName {
			return nil, nil, apperr.PermissionDenied(apperr.ReasonScopeMissing, "token is not valid for namespace %q", name)
		}
		ns, ok := cat.Namespace(p.NamespaceID)
		if !ok {
			return nil, nil, apperr.NotFound("namespace not found")
		}
		return p, ns, nil
	}
	if name == "" {
		return nil, nil, apperr.InvalidArgument("", "namespace is required")
	}
	if p.TenantID == "" {
		return nil, nil, apperr.InvalidArgument("", "active tenant is required (X-Spinneret-Tenant header)")
	}
	ns, ok := cat.NamespaceByName(p.TenantID, name)
	if !ok {
		return nil, nil, apperr.NotFound("namespace %q not found", name)
	}
	return p, ns, nil
}

// Site resolves a site by name inside a namespace snapshot.
func Site(ns *catalog.Namespace, name string) (*catalog.Site, error) {
	if name == "" {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site is required")
	}
	s, ok := ns.Sites[name]
	if !ok {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", name)
	}
	return s, nil
}

// NamespaceResource builds an authorization resource for a namespace-level object.
func NamespaceResource(ns *catalog.Namespace) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
}

// SiteResource builds an authorization resource for a site-level object.
func SiteResource(ns *catalog.Namespace, s *catalog.Site) authz.Resource {
	r := NamespaceResource(ns)
	r.SiteID = s.ID
	r.SiteName = s.Name
	return r
}

// Timestamp converts a time to a protobuf timestamp; the zero time maps to nil.
func Timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// TimestampPtr converts an optional time to a protobuf timestamp.
func TimestampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

// MillisTimestamp converts Unix milliseconds to a protobuf timestamp; values <= 0 map to nil.
func MillisTimestamp(ms int64) *timestamppb.Timestamp {
	if ms <= 0 {
		return nil
	}
	return timestamppb.New(time.UnixMilli(ms))
}

// Time converts an optional protobuf timestamp to *time.Time.
func Time(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// Struct converts a map to a protobuf Struct. Besides the types accepted by
// structpb.NewValue, values may be typed maps with string keys and typed
// slices (converted element by element), time.Time and *time.Time (RFC 3339
// strings in UTC, the zero time and nil pointers become null) and
// json.RawMessage (decoded). A nil map yields a nil Struct.
func Struct(m map[string]any) (*structpb.Struct, error) {
	if m == nil {
		return nil, nil
	}
	s, err := structpb.NewStruct(normalizeForStruct(m).(map[string]any))
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("convert to struct: %w", err))
	}
	return s, nil
}

// normalizeForStruct converts values structpb does not accept (typed maps
// and slices, times, raw JSON) into the generic JSON shapes it does accept.
// Nested nil maps and slices become null, like encoding/json renders them.
// Unsupported values are returned unchanged so that structpb reports them.
func normalizeForStruct(v any) any {
	switch t := v.(type) {
	case nil, bool, string, float64, []byte, json.Number:
		return v
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case float32:
		return float64(t)
	case time.Time:
		return timeForStruct(t)
	case *time.Time:
		if t == nil {
			return nil
		}
		return timeForStruct(*t)
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(t, &decoded); err != nil {
			return v
		}
		return decoded
	case map[string]any:
		return mapForStruct(t)
	case []any:
		return sliceForStruct(t)
	case map[string]string:
		return mapForStruct(t)
	case map[string]int:
		return mapForStruct(t)
	case map[string]int64:
		return mapForStruct(t)
	case map[string]float64:
		return mapForStruct(t)
	case map[string]bool:
		return mapForStruct(t)
	case []string:
		return sliceForStruct(t)
	case []int:
		return sliceForStruct(t)
	case []int64:
		return sliceForStruct(t)
	case []float64:
		return sliceForStruct(t)
	case []map[string]any:
		return sliceForStruct(t)
	default:
		return reflectForStruct(v)
	}
}

// timeForStruct renders a time as an RFC 3339 UTC string (null for the zero time).
func timeForStruct(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func mapForStruct[V any](m map[string]V) any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = normalizeForStruct(val)
	}
	return out
}

func sliceForStruct[E any](s []E) any {
	if s == nil {
		return nil
	}
	out := make([]any, len(s))
	for i, val := range s {
		out[i] = normalizeForStruct(val)
	}
	return out
}

// reflectForStruct handles the remaining string-keyed maps, slices, arrays
// and pointers generically. Anything else is returned unchanged.
func reflectForStruct(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = normalizeForStruct(iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := range rv.Len() {
			out[i] = normalizeForStruct(rv.Index(i).Interface())
		}
		return out
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return normalizeForStruct(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Bool:
		return rv.Bool()
	case reflect.String:
		return rv.String()
	default:
		return v
	}
}

// Duration parses an admin API duration string ("30m", "7d", "permanent").
// allowPermanent controls whether "permanent" is accepted.
func Duration(field, s string, allowPermanent bool) (durationx.Duration, error) {
	d, err := durationx.Parse(s)
	if err != nil {
		return 0, apperr.InvalidArgument("", "%s: %v", field, err)
	}
	if d.IsPermanent() && !allowPermanent {
		return 0, apperr.InvalidArgument("", "%s: permanent is not allowed", field)
	}
	return d, nil
}
