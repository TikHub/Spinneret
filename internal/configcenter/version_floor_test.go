package configcenter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

// TestRecreatedItemNeverReusesVersions is the regression test for nodes that
// held version N of a deleted item: the re-created item started again at
// version 1, so a node holding version 1 never received its new content.
func TestRecreatedItemNeverReusesVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	old := e.create(t, "crawler", "a.json", FormatJSON, `{"old":true}`, true)
	require.Equal(t, int32(1), old.CurrentVersion)
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: "crawler", Key: "a.json", Version: 1}}

	res := watchAsync(e.svc, node, e.ns, refs, 30*time.Second)
	waitWatchers(t, e.svc, 1)
	require.NoError(t, e.svc.DeleteItem(ctx, e.admin, old.ID))
	recreatedAt := time.Now()
	recreated := e.create(t, "crawler", "a.json", FormatJSON, `{"new":true}`, true)
	require.Equal(t, int32(2), recreated.CurrentVersion, "a re-created item continues above the deleted item's versions")

	select {
	case r := <-res:
		require.NoError(t, r.err)
		require.Less(t, time.Since(recreatedAt), time.Second, "watchers must wake within 1s")
		require.Len(t, r.items, 1)
		require.Equal(t, int32(2), r.items[0].Version)
		require.JSONEq(t, `{"new":true}`, r.items[0].Content)
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher holding the deleted item's version was not woken")
	}

	// A new poll holding the deleted item's version returns the new content at once.
	items, err := e.svc.WatchConfig(ctx, node, e.ns, refs, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int32(2), items[0].Version)
	require.JSONEq(t, `{"new":true}`, items[0].Content)

	// The audit entry and the published event carry the real version.
	created := e.audit.find(ActionCreate, audit.ResultOK)
	require.EqualValues(t, 2, created[len(created)-1].Details["version"])
	cfgEvents := e.events.on(events.ChannelConfig)
	var data ConfigEventData
	require.NoError(t, json.Unmarshal(cfgEvents[len(cfgEvents)-1].Data, &data))
	require.Equal(t, int32(2), data.Version)
}

// TestVersionFloorAcrossDraftsPublishesAndRollbacks covers items re-created
// as drafts, several delete/re-create cycles and rollbacks.
func TestVersionFloorAcrossDraftsPublishesAndRollbacks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()

	first := e.create(t, "crawler", "a.json", FormatJSON, `{"v":1}`, true)
	require.Equal(t, int32(2), e.publishContent(t, first.ID, `{"v":2}`))
	require.Equal(t, int32(3), e.publishContent(t, first.ID, `{"v":3}`))
	require.NoError(t, e.svc.DeleteItem(ctx, e.admin, first.ID))

	// Re-created as a draft: the first publish continues above version 3.
	draft := e.create(t, "crawler", "a.json", FormatJSON, `{"v":"draft"}`, false)
	require.Zero(t, draft.CurrentVersion)
	_, v, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: draft.ID})
	require.NoError(t, err)
	require.Equal(t, int32(4), v)
	require.Equal(t, int32(5), e.publishContent(t, draft.ID, `{"v":5}`))
	_, v, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: draft.ID, Version: 4})
	require.NoError(t, err)
	require.Equal(t, int32(6), v)
	page, err := e.svc.ListVersions(ctx, e.admin, draft.ID, 10, "")
	require.NoError(t, err)
	require.Equal(t, 3, page.Total, "versions of the deleted item are not listed")

	// A never-published re-created item that is deleted keeps the floor.
	require.NoError(t, e.svc.DeleteItem(ctx, e.admin, draft.ID))
	unpublished := e.create(t, "crawler", "a.json", FormatText, "", false)
	require.NoError(t, e.svc.DeleteItem(ctx, e.admin, unpublished.ID))
	again := e.create(t, "crawler", "a.json", FormatText, "again", true)
	require.Equal(t, int32(7), again.CurrentVersion)

	// Other keys and namespaces are not affected.
	require.Equal(t, int32(1), e.create(t, "crawler", "b.json", FormatJSON, `{}`, true).CurrentVersion)
	other, err := e.svc.CreateItem(ctx, userPrincipal(e.otherNS.TenantID, "admin"), e.otherNS, CreateRequest{
		Group: "crawler", Key: "a.json", Format: FormatJSON, Content: `{}`, Publish: true,
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), other.CurrentVersion)
}
