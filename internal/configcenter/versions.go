package configcenter

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/configcenter/configdb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/textdiff"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

type versionCursor struct {
	Before int32 `json:"b"`
}

// ListVersions lists published versions of an item, newest first
// (config:read). A page ends early when its combined content would exceed
// Config.MaxResponseBytes (at least one version is always returned).
func (s *Service) ListVersions(ctx context.Context, p *authz.Principal, id string, size int, token string) (VersionPage, error) {
	h, err := s.loadHeader(ctx, p, id)
	if err != nil {
		return VersionPage{}, err
	}
	if err := p.Require(authz.PermConfigRead, configResource(h.NS, h.Group)); err != nil {
		return VersionPage{}, err
	}
	var cur versionCursor
	if err := decodeCursor(token, &cur); err != nil {
		return VersionPage{}, err
	}
	if cur.Before < 0 || (token != "" && cur.Before == 0) {
		return VersionPage{}, apperr.InvalidArgument("", "invalid page_token")
	}
	size = pageSize(size)
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	q := s.queries()
	rows, err := q.ConfigListVersions(ctx, configdb.ConfigListVersionsParams{
		ItemID: h.ID, BeforeVersion: cur.Before, PageLimit: int32(size + 1),
	})
	if err != nil {
		return VersionPage{}, wrapErr(err, "list config versions")
	}
	more := len(rows) > size
	if more {
		rows = rows[:size]
	}
	var total int64
	for i, r := range rows {
		if i > 0 && total+r.ContentBytes > s.cfg.MaxResponseBytes {
			rows, more = rows[:i], true
			break
		}
		total += r.ContentBytes
	}
	page := VersionPage{Versions: make([]Version, 0, len(rows))}
	if more && len(rows) > 0 {
		page.NextPageToken = encodeCursor(versionCursor{Before: rows[len(rows)-1].Version})
	}
	contents, err := s.versionContents(ctx, q, h.ID, rows)
	if err != nil {
		return VersionPage{}, err
	}
	for _, r := range rows {
		c, ok := contents[r.Version]
		if !ok {
			continue
		}
		v := Version{Version: r.Version, Content: c, Comment: r.Comment, PublishedBy: r.PublishedBy, PublishedAt: r.PublishedAt}
		if r.SourceVersion != nil {
			v.SourceVersion = *r.SourceVersion
		}
		page.Versions = append(page.Versions, v)
	}
	count, err := q.ConfigCountVersions(ctx, h.ID)
	if err != nil {
		return VersionPage{}, wrapErr(err, "count config versions")
	}
	page.Total = int(count)
	return page, nil
}

func (s *Service) versionContents(ctx context.Context, q *configdb.Queries, itemID string, rows []configdb.ConfigListVersionsRow) (map[int32]string, error) {
	if len(rows) == 0 {
		return map[int32]string{}, nil
	}
	ids := make([]string, len(rows))
	versions := make([]int32, len(rows))
	for i, r := range rows {
		ids[i], versions[i] = itemID, r.Version
	}
	loaded, err := q.ConfigVersionContents(ctx, configdb.ConfigVersionContentsParams{ItemIds: ids, Versions: versions})
	if err != nil {
		return nil, wrapErr(err, "load config version contents")
	}
	out := make(map[int32]string, len(loaded))
	for _, r := range loaded {
		out[r.Version] = r.Content
	}
	return out, nil
}

// DiffVersions compares two sides of an item (config:read). from 0 is the
// current published version; to 0 is the draft, or the current published
// version when there is no draft. A side that was never published is empty.
func (s *Service) DiffVersions(ctx context.Context, p *authz.Principal, id string, from, to int32) (Diff, error) {
	h, err := s.loadHeader(ctx, p, id)
	if err != nil {
		return Diff{}, err
	}
	if err := p.Require(authz.PermConfigRead, configResource(h.NS, h.Group)); err != nil {
		return Diff{}, err
	}
	if from < 0 || to < 0 {
		return Diff{}, apperr.InvalidArgument("", "versions must not be negative")
	}
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	q := s.queries()
	view, err := q.ConfigGetItemView(ctx, h.ID)
	if err != nil {
		return Diff{}, pgstore.MapError(err, "config item")
	}
	published, publishedName := deref(view.PublishedContent), "published"
	if view.CurrentVersion > 0 {
		publishedName = versionName(view.CurrentVersion)
	}
	fromContent, fromName := published, publishedName
	if from > 0 {
		if fromContent, err = versionContent(ctx, q, h.ID, from); err != nil {
			return Diff{}, err
		}
		fromName = versionName(from)
	}
	toContent, toName := published, publishedName
	switch {
	case to > 0:
		if toContent, err = versionContent(ctx, q, h.ID, to); err != nil {
			return Diff{}, err
		}
		toName = versionName(to)
	case view.DraftContent != nil:
		toContent, toName = *view.DraftContent, "draft"
	}
	return Diff{
		FromContent: fromContent,
		ToContent:   toContent,
		Unified:     textdiff.Unified(fromName, toName, fromContent, toContent, defaultDiffContext),
	}, nil
}

func versionContent(ctx context.Context, q *configdb.Queries, itemID string, version int32) (string, error) {
	v, err := q.ConfigGetVersion(ctx, configdb.ConfigGetVersionParams{ItemID: itemID, Version: version})
	if err != nil {
		return "", pgstore.MapError(err, fmt.Sprintf("config version %d", version))
	}
	return v.Content, nil
}

func versionName(v int32) string {
	return "v" + strconv.FormatInt(int64(v), 10)
}
