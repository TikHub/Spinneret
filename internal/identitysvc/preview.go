package identitysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
)

// MaxPreviewPayloadBytes is the maximum size of a preview sample payload.
const MaxPreviewPayloadBytes = 1 << 20

// previewTypeID is the ID draft specs are compiled under.
const previewTypeID = "ity_preview"

// PreviewDelivery normalizes a sample payload and renders the credential a
// node would receive, with an existing type (TypeID) or a draft spec
// (SpecYAML; the site is in.Site or, when empty, the site of the spec). It
// requires identity:read or site:read on the site. Nothing is stored.
//
// secret_ref values are never resolved: they render as the literal
// placeholder "<secret:PATH>", so the preview cannot be used to read secrets.
// Spec, payload and rendering problems are returned in PreviewResult.Errors.
func (s *Service) PreviewDelivery(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in PreviewInput) (PreviewResult, error) {
	if (in.TypeID == "") == (in.SpecYAML == "") {
		return PreviewResult{}, invalid("exactly one of type_id and spec_yaml is required")
	}
	if len(in.SpecYAML) > MaxSpecYAMLBytes {
		return PreviewResult{}, invalid("spec_yaml must be at most %d bytes", MaxSpecYAMLBytes)
	}
	if len(in.PayloadJSON) > MaxPreviewPayloadBytes {
		return PreviewResult{}, invalid("payload_json must be at most %d bytes", MaxPreviewPayloadBytes)
	}
	var (
		ct  *identity.CompiledType
		res PreviewResult
	)
	if in.TypeID != "" {
		site, err := s.previewTypeSite(ctx, p, ns, in.TypeID)
		if err != nil {
			return PreviewResult{}, err
		}
		if ct, err = s.compiledType(ctx, identitysvcdb.New(s.pool), site, in.TypeID); err != nil {
			return PreviewResult{}, err
		}
	} else {
		siteName := in.Site
		if siteName == "" {
			siteName = yamlSiteName([]byte(in.SpecYAML))
		}
		site, err := siteByName(ns, siteName)
		if err != nil {
			return PreviewResult{}, err
		}
		if err := requireAny(p, ns, site, authz.PermIdentityRead, authz.PermSiteRead); err != nil {
			return PreviewResult{}, err
		}
		if ct, res.Errors = compileDraft(site, in.SpecYAML); len(res.Errors) > 0 {
			return res, nil
		}
	}
	payload, err := decodePayloadObject(in.PayloadJSON)
	if err != nil {
		res.Errors = append(res.Errors, errorMessage(err))
		return res, nil
	}
	normalized, err := ct.Normalize(payload)
	if err != nil {
		res.Errors = append(res.Errors, errorMessage(err))
		return res, nil
	}
	res.Normalized = normalized
	cred, _, err := ct.Render(ctx, normalized, placeholderSecret)
	if err != nil {
		res.Errors = append(res.Errors, errorMessage(err))
		return res, nil
	}
	res.Credential = cred
	return res, nil
}

// previewTypeSite finds the site of an existing identity type of ns and
// checks read access.
func (s *Service) previewTypeSite(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, typeID string) (*catalog.Site, error) {
	for _, site := range ns.SitesByID {
		if _, ok := site.IdentityTypesByID[typeID]; ok {
			return site, requireAny(p, ns, site, authz.PermIdentityRead, authz.PermSiteRead)
		}
	}
	if s.pool == nil {
		return nil, apperr.Internal(errNoPool)
	}
	row, err := identitysvcdb.New(s.pool).IdentityTypeSpecByID(ctx, typeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("identity type not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load identity type %s: %w", truncateText(typeID, 64), err)
	}
	site, ok := ns.SitesByID[row.SiteID]
	if !ok {
		return nil, apperr.NotFound("identity type not found")
	}
	return site, requireAny(p, ns, site, authz.PermIdentityRead, authz.PermSiteRead)
}

// compileDraft parses and compiles a draft spec for site, returning the
// problems as messages.
func compileDraft(site *catalog.Site, specYAML string) (*identity.CompiledType, []string) {
	spec, _, err := parseTypeYAMLForSite([]byte(specYAML), site.Name)
	if err != nil {
		return nil, []string{errorMessage(err)}
	}
	if !site.HasClient(spec.Client) {
		return nil, []string{"client \"" + spec.Client + "\" is not a client of site \"" + site.Name + "\""}
	}
	ct, err := identity.Compile(previewTypeID, site.ID, 1, spec)
	if err != nil {
		return nil, []string{errorMessage(err)}
	}
	return ct, nil
}

// placeholderSecret renders secret references as "<secret:PATH>".
func placeholderSecret(_ context.Context, path string) (string, error) {
	return "<secret:" + path + ">", nil
}

// decodePayloadObject decodes a JSON object keeping numbers exact.
func decodePayloadObject(data string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, invalid("payload is not valid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("payload is not valid JSON: unexpected data after the object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("payload must be a JSON object")
	}
	return obj, nil
}

// errorMessage returns the client-facing message of err.
func errorMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}
