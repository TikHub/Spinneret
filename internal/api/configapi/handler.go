// Package configapi implements the Connect handlers of the config center:
// ConfigService (node reads and long-poll watches) and ConfigAdminService
// (items, drafts, versions, publish, rollback, diff). Permission checks,
// validation, auditing and events live in internal/configcenter; handlers
// resolve the principal and namespace and convert messages.
package configapi

import (
	"time"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter"
)

// Handler implements ConfigServiceHandler and ConfigAdminServiceHandler.
type Handler struct {
	svc *configcenter.Service
	cat catalog.Catalog
}

var (
	_ spinneretv1connect.ConfigServiceHandler      = (*Handler)(nil)
	_ spinneretv1connect.ConfigAdminServiceHandler = (*Handler)(nil)
)

// New creates the config handlers.
func New(svc *configcenter.Service, cat catalog.Catalog) *Handler {
	return &Handler{svc: svc, cat: cat}
}

func itemInfo(it configcenter.Item) *spinneretv1.ConfigItemInfo {
	info := &spinneretv1.ConfigItemInfo{
		Id:               it.ID,
		Namespace:        it.NamespaceName,
		Group:            it.Group,
		Key:              it.Key,
		Format:           it.Format,
		SchemaJson:       string(it.Schema),
		Description:      it.Description,
		CurrentVersion:   it.CurrentVersion,
		PublishedContent: it.PublishedContent,
		HasDraft:         it.HasDraft,
		DraftUpdatedBy:   it.DraftUpdatedBy,
		DraftUpdatedAt:   apiutil.TimestampPtr(it.DraftUpdatedAt),
		PublishedBy:      it.PublishedBy,
		PublishedAt:      apiutil.TimestampPtr(it.PublishedAt),
		CreatedAt:        apiutil.Timestamp(it.CreatedAt),
		UpdatedAt:        apiutil.Timestamp(it.UpdatedAt),
	}
	if it.DraftContent != nil {
		info.DraftContent = *it.DraftContent
	}
	return info
}

func versionInfo(v configcenter.Version) *spinneretv1.ConfigVersion {
	return &spinneretv1.ConfigVersion{
		Version:       v.Version,
		Content:       v.Content,
		Comment:       v.Comment,
		SourceVersion: v.SourceVersion,
		PublishedBy:   v.PublishedBy,
		PublishedAt:   apiutil.Timestamp(v.PublishedAt),
	}
}

func nodeItem(it configcenter.NodeItem) *spinneretv1.ConfigItem {
	return &spinneretv1.ConfigItem{
		Namespace:     it.Namespace,
		Group:         it.Group,
		Key:           it.Key,
		Format:        it.Format,
		Version:       it.Version,
		Content:       it.Content,
		UpdatedAt:     apiutil.TimestampPtr(it.UpdatedAt),
		HasSecretRefs: it.HasSecretRefs,
	}
}

func nodeItems(items []configcenter.NodeItem) []*spinneretv1.ConfigItem {
	out := make([]*spinneretv1.ConfigItem, len(items))
	for i, it := range items {
		out[i] = nodeItem(it)
	}
	return out
}

func timeoutFromMillis(ms int32) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
