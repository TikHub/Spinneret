package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// UpdateRequest changes proxy attributes; nil fields are left unchanged.
type UpdateRequest struct {
	// URL replaces the proxy URL including credentials; the URL is re-sealed
	// and url_version incremented when it changes.
	URL            *string
	Kind           *string
	Region         *string
	City           *string
	Provider       *string
	MaxConcurrency *int
	// Tags replace the tags when non-nil (an empty slice clears them).
	Tags            []string
	SetTags         bool
	SessionTemplate *string
}

// UpdateProxy changes a proxy (proxy:write).
func (s *Service) UpdateProxy(ctx context.Context, p *authz.Principal, id string, req UpdateRequest) (*Proxy, error) {
	var (
		updated proxydb.Proxy
		ns      *catalog.Namespace
		changed []string
	)
	if err := requireTokenScope(s.cat, p, authz.PermProxyWrite); err != nil {
		return nil, err
	}
	err := inTx(ctx, s.pool, func(q *proxydb.Queries) error {
		row, err := q.ProxyGetForUpdate(ctx, id)
		if err != nil {
			return postgres.MapError(err, "proxy")
		}
		if ns, err = authorizeRow(s.cat, p, row, authz.PermProxyWrite); err != nil {
			return err
		}
		var newURL *ParsedURL
		if req.URL != nil {
			u, err := ParseProxyURL(*req.URL)
			if err != nil {
				return apperr.InvalidArgument("", "url: %v", err)
			}
			newURL = &u
		}
		arg, fields, err := s.buildUpdate(row, newURL, req)
		if err != nil {
			return err
		}
		changed = fields
		if len(fields) == 0 {
			updated = row
			return nil
		}
		updated, err = q.ProxyUpdate(ctx, arg)
		return err
	})
	if err != nil {
		return nil, postgres.MapError(err, "proxy")
	}

	view := newProxyView(updated, ns)
	if len(changed) > 0 {
		sctx, cancel := detached(ctx)
		defer cancel()
		s.record(sctx, p, ns, "proxy.update", updated.ID, updated.DisplayUrl, audit.ResultOK, map[string]any{"fields": changed})
		if err := publishState(sctx, s.bus, ns, []StateEventData{{
			SubjectID: updated.ID, From: updated.State, To: updated.State, Action: "update",
		}}); err != nil {
			s.logger.Warn("proxy update: publish event failed", slog.String("proxy_id", id), slog.Any("error", err))
		}
		if err := s.syncProxies(sctx, ns.ID, []string{updated.ID}); err != nil {
			s.logger.Error("proxy update: hot state sync failed", slog.String("proxy_id", id), slog.Any("error", err))
			return nil, apperr.Internal(err)
		}
	}
	if err := s.decorate(ctx, p, ns, []*Proxy{view}); err != nil {
		return nil, err
	}
	return view, nil
}

// buildUpdate computes the new row values and the names of changed fields.
func (s *Service) buildUpdate(row proxydb.Proxy, newURL *ParsedURL, req UpdateRequest) (proxydb.ProxyUpdateParams, []string, error) {
	arg := proxydb.ProxyUpdateParams{
		ID: row.ID, Scheme: row.Scheme, Host: row.Host, Port: row.Port, UsernameHint: row.UsernameHint,
		DisplayUrl: row.DisplayUrl, UrlHash: row.UrlHash, UrlCiphertext: row.UrlCiphertext,
		UrlWrappedDek: row.UrlWrappedDek, UrlKekID: row.UrlKekID, UrlVersion: row.UrlVersion,
	}
	var fields []string
	if newURL != nil {
		hash := URLHash(s.pepper, *newURL)
		if !bytes.Equal(hash, row.UrlHash) {
			sealed, err := SealURL(s.cipher, row.ID, *newURL)
			if err != nil {
				return arg, nil, fmt.Errorf("seal proxy url: %w", err)
			}
			arg.Scheme, arg.Host, arg.Port = newURL.Scheme, newURL.Host, int32(newURL.Port)
			arg.UsernameHint, arg.DisplayUrl, arg.UrlHash = newURL.UsernameHint(), newURL.DisplayURL(), hash
			arg.UrlCiphertext, arg.UrlWrappedDek, arg.UrlKekID = sealed.Ciphertext, sealed.WrappedDEK, sealed.KEKID
			arg.UrlVersion = row.UrlVersion + 1
			fields = append(fields, "url")
		}
	}

	current := rowAttributes(row)
	next := current
	if req.Kind != nil {
		next.Kind = strings.ToLower(strings.TrimSpace(*req.Kind))
	}
	if req.Region != nil {
		next.Region = strings.TrimSpace(*req.Region)
	}
	if req.City != nil {
		next.City = strings.TrimSpace(*req.City)
	}
	if req.Provider != nil {
		next.Provider = strings.TrimSpace(*req.Provider)
	}
	if req.MaxConcurrency != nil {
		next.MaxConcurrency = *req.MaxConcurrency
	}
	if req.SetTags {
		next.Tags = nonNil(req.Tags)
	}
	if req.SessionTemplate != nil {
		next.SessionTemplate = *req.SessionTemplate
	}
	if err := next.validate(); err != nil {
		return arg, nil, apperr.InvalidArgument("", "%v", err)
	}
	fields = append(fields, changedAttributes(current, next)...)
	arg.Kind, arg.Region, arg.City, arg.Provider = next.Kind, next.Region, next.City, next.Provider
	arg.MaxConcurrency, arg.Tags, arg.SessionTemplate = int32(next.MaxConcurrency), next.Tags, next.SessionTemplate
	return arg, fields, nil
}

// changedAttributes lists the attribute names that differ.
func changedAttributes(a, b Attributes) []string {
	var out []string
	add := func(name string, differ bool) {
		if differ {
			out = append(out, name)
		}
	}
	add("kind", a.Kind != b.Kind)
	add("region", a.Region != b.Region)
	add("city", a.City != b.City)
	add("provider", a.Provider != b.Provider)
	add("max_concurrency", a.MaxConcurrency != b.MaxConcurrency)
	add("tags", !(Attributes{Tags: a.Tags}).equal(Attributes{Tags: b.Tags}))
	add("session_template", a.SessionTemplate != b.SessionTemplate)
	return out
}

// encodeCursor serializes an opaque pagination cursor.
func encodeCursor(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeCursor parses a cursor produced by encodeCursor.
func decodeCursor(token string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, v) != nil {
		return apperr.InvalidArgument("", "invalid page_token")
	}
	return nil
}
