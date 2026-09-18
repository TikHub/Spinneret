package leaseapi

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/scheduler"
)

// grantProto converts an issued lease into its API response.
func grantProto(g *scheduler.Grant) (*spinneretv1.AcquireResponse, error) {
	if g == nil {
		return nil, internalError(fmt.Errorf("nil grant"))
	}
	cred, err := credentialProto(g.Credential)
	if err != nil {
		return nil, internalError(fmt.Errorf("convert credential of lease %s: %w", g.Lease.ID, err))
	}
	return &spinneretv1.AcquireResponse{
		Lease: &spinneretv1.Lease{
			LeaseId:       g.Lease.ID,
			IdentityId:    g.Lease.IdentityID,
			IdentityType:  g.Lease.IdentityType,
			EndpointGroup: g.Lease.EndpointGroup,
			ExpiresAt:     apiutil.Timestamp(g.Lease.ExpiresAt),
			Sticky:        g.Lease.Sticky,
			Probe:         g.Lease.Probe,
		},
		Credential: cred,
		Proxy:      proxyProto(g.Proxy),
		Hints:      &spinneretv1.Hints{RenewBeforeMs: clampInt32(g.RenewBefore.Milliseconds())},
	}, nil
}

// proxyProto converts the proxy assignment; nil (no proxy) stays nil so the
// JSON response carries "proxy": null.
func proxyProto(p *scheduler.ProxyAssignment) *spinneretv1.ProxyAssignment {
	if p == nil {
		return nil
	}
	return &spinneretv1.ProxyAssignment{ProxyId: p.ID, Url: p.URL, Kind: p.Kind, Region: p.Region}
}

// credentialProto converts a rendered credential. Unused segments are empty
// maps, an empty cookie header, and null json/values.
func credentialProto(c *identity.Credential) (*spinneretv1.Credential, error) {
	out := &spinneretv1.Credential{
		Cookies: map[string]string{},
		Headers: map[string]string{},
		Query:   map[string]string{},
	}
	if c == nil {
		return out, nil
	}
	if c.Cookies != nil {
		out.Cookies = c.Cookies
	}
	if c.Headers != nil {
		out.Headers = c.Headers
	}
	if c.Query != nil {
		out.Query = c.Query
	}
	out.CookieHeader = c.CookieHeader
	if c.JSON != nil {
		v, err := toValue(c.JSON)
		if err != nil {
			return nil, fmt.Errorf("json segment: %w", err)
		}
		out.Json = v
	}
	if c.Values != nil {
		s, err := toStruct(c.Values)
		if err != nil {
			return nil, fmt.Errorf("values segment: %w", err)
		}
		out.Values = s
	}
	return out, nil
}

// toValue converts an arbitrary JSON-compatible Go value. Values that
// structpb cannot take directly (typed maps and slices, json.Number, ...) are
// normalized through a JSON round trip.
func toValue(v any) (*structpb.Value, error) {
	if pv, err := structpb.NewValue(v); err == nil {
		return pv, nil
	}
	generic, err := jsonRoundTrip(v)
	if err != nil {
		return nil, err
	}
	return structpb.NewValue(generic)
}

// toStruct converts a map into a Struct with the same normalization as toValue.
func toStruct(m map[string]any) (*structpb.Struct, error) {
	if s, err := structpb.NewStruct(m); err == nil {
		return s, nil
	}
	generic, err := jsonRoundTrip(m)
	if err != nil {
		return nil, err
	}
	obj, ok := generic.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("values are not a JSON object")
	}
	return structpb.NewStruct(obj)
}

func jsonRoundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}
