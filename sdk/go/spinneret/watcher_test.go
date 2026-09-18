package spinneret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

// configServer is a fake published config store driving WatchConfig.
type configServer struct {
	mu      sync.Mutex
	items   map[ConfigKey]*ConfigItem
	changed chan struct{}
}

func newConfigServer(items ...*ConfigItem) *configServer {
	s := &configServer{items: map[ConfigKey]*ConfigItem{}, changed: make(chan struct{})}
	for _, item := range items {
		s.items[ConfigKey{Group: item.GetGroup(), Key: item.GetKey()}] = item
	}
	return s
}

func (s *configServer) publish(item *ConfigItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[ConfigKey{Group: item.GetGroup(), Key: item.GetKey()}] = item
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *configServer) install(f *fakeNode) {
	f.set(func(f *fakeNode) {
		f.batchGetConfig = func(_ context.Context, req *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			out := &BatchGetConfigResponse{}
			for _, ref := range req.GetItems() {
				if item, ok := s.items[ConfigKey{Group: ref.GetGroup(), Key: ref.GetKey()}]; ok {
					out.Items = append(out.Items, item)
				} else {
					out.Missing = append(out.Missing, ref)
				}
			}
			return out, nil
		}
		f.watchConfig = func(ctx context.Context, req *WatchConfigRequest) (*WatchConfigResponse, error) {
			deadline := time.After(time.Duration(req.GetTimeoutMs()) * time.Millisecond)
			for {
				s.mu.Lock()
				out := &WatchConfigResponse{}
				for _, w := range req.GetItems() {
					item, ok := s.items[ConfigKey{Group: w.GetGroup(), Key: w.GetKey()}]
					if ok && item.GetVersion() != w.GetVersion() {
						out.Items = append(out.Items, item)
					}
				}
				changed := s.changed
				s.mu.Unlock()
				if len(out.Items) > 0 {
					return out, nil
				}
				select {
				case <-changed:
				case <-deadline:
					return out, nil
				case <-ctx.Done():
					return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
				}
			}
		}
	})
}

func TestConfigWatcherLifecycle(t *testing.T) {
	for _, tc := range protocolCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			store := newConfigServer(testConfigItem("crawler", "search.json", 1, `{"requests":3}`))
			store.install(f)
			srv := newFakeServer(t, f, nil)
			c := newTestClient(t, srv.URL, func(o *Options) { o.UseGRPC = tc.useGRPC })
			dir := t.TempDir()

			var mu sync.Mutex
			var seen []string
			record := func(item *ConfigItem) {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, fmt.Sprintf("%s@%d", item.GetKey(), item.GetVersion()))
			}
			w, err := c.NewConfigWatcher(WatcherOptions{
				Items:       []ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "crawler", Key: "later.json"}, {Group: "crawler", Key: "search.json"}},
				Timeout:     200 * time.Millisecond,
				SnapshotDir: dir,
				OnChange:    record,
			})
			require.NoError(t, err)
			require.Len(t, w.Keys(), 2, "duplicates removed")
			w.AddListener(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, w.Start(ctx))
			require.Error(t, w.Start(ctx), "already started")

			item, ok := w.Get("crawler", "search.json")
			require.True(t, ok)
			require.Equal(t, `{"requests":3}`, item.GetContent())
			_, ok = w.Get("crawler", "later.json")
			require.False(t, ok)
			require.False(t, w.FromSnapshot())
			require.Equal(t, map[ConfigKey]int32{{Group: "crawler", Key: "search.json"}: 1}, w.Versions())
			mu.Lock()
			require.Equal(t, []string{"search.json@1"}, seen, "initial values are passed to listeners")
			mu.Unlock()

			// A publish reaches WaitForChange and the listeners.
			var waited atomic.Pointer[ConfigItem]
			waitDone := make(chan struct{})
			go func() {
				defer close(waitDone)
				changed, err := w.WaitForChange(ctx, "crawler", "later.json")
				if err == nil {
					waited.Store(changed)
				}
			}()
			time.Sleep(20 * time.Millisecond)
			store.publish(testConfigItem("crawler", "search.json", 2, `{"requests":5}`))
			store.publish(testConfigItem("crawler", "later.json", 7, `{}`))
			<-waitDone
			require.Equal(t, int32(7), waited.Load().GetVersion())
			eventually(t, 2*time.Second, func() bool {
				v := w.Versions()
				return v[ConfigKey{Group: "crawler", Key: "search.json"}] == 2
			}, "search.json version 2")
			require.Len(t, w.Items(), 2)

			// Snapshots of both items are on disk.
			snap := newSnapshotStore(dir, c.s.host, "", nil, discardLogger())
			eventually(t, 2*time.Second, func() bool {
				loaded, err := snap.load("crawler", "search.json")
				return err == nil && loaded.GetVersion() == 2
			}, "snapshot of version 2")

			// The watch requests carry the known versions.
			f.mu.Lock()
			last := f.watches[len(f.watches)-1]
			f.mu.Unlock()
			require.Equal(t, int32(200), last.GetTimeoutMs())
			require.Len(t, last.GetItems(), 2)

			// Cancelling the Start context stops the loop.
			cancel()
			select {
			case <-w.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("watcher did not stop")
			}
			_, err = w.WaitForChange(context.Background(), "", "")
			require.ErrorContains(t, err, "stopped")
			w.Stop()
			w.Stop()
			require.Error(t, w.Start(context.Background()), "a stopped watcher cannot restart")
		})
	}
}

func TestConfigWatcherSnapshotFallback(t *testing.T) {
	dir := t.TempDir()
	f := newFakeNode()
	var failStart atomic.Bool
	failStart.Store(true)
	store := newConfigServer(testConfigItem("crawler", "search.json", 4, `{"v":4}`))
	store.install(f)
	realBatch := f.batchGetConfig
	f.set(func(f *fakeNode) {
		f.batchGetConfig = func(ctx context.Context, req *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
			if failStart.Load() {
				return nil, apiError(connect.CodeUnavailable, ReasonRebuilding, 0, "down")
			}
			return realBatch(ctx, req)
		}
	})
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, func(o *Options) { o.Retry = NoRetry() })

	// Seed a snapshot as an earlier run would have.
	seed := newSnapshotStore(dir, c.s.host, "", nil, discardLogger())
	_, err := seed.save(testConfigItem("crawler", "search.json", 3, `{"v":3}`))
	require.NoError(t, err)

	var changes atomic.Int32
	w, err := c.NewConfigWatcher(WatcherOptions{
		Items:       []ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "crawler", Key: "none.json"}},
		Timeout:     100 * time.Millisecond,
		SnapshotDir: dir,
		OnChange:    func(*ConfigItem) { changes.Add(1) },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, w.Start(ctx))
	require.True(t, w.FromSnapshot())
	item, ok := w.Get("crawler", "search.json")
	require.True(t, ok)
	require.Equal(t, int32(3), item.GetVersion())

	// The first successful poll replaces the snapshot values.
	eventually(t, 2*time.Second, func() bool { return !w.FromSnapshot() }, "server answered")
	eventually(t, 2*time.Second, func() bool {
		it, _ := w.Get("crawler", "search.json")
		return it.GetVersion() == 4
	}, "fresh version")
	eventually(t, 2*time.Second, func() bool { return changes.Load() >= 2 }, "listeners saw both values")
	w.Stop()
	require.Error(t, w.Start(ctx))
}

func TestConfigWatcherStartErrors(t *testing.T) {
	f := newFakeNode()
	var denied atomic.Bool
	denied.Store(true)
	f.batchGetConfig = func(_ context.Context, req *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
		if denied.Load() {
			return nil, apiError(connect.CodePermissionDenied, ReasonScopeMissing, 0, "config:read missing")
		}
		return &BatchGetConfigResponse{Items: []*ConfigItem{testConfigItem("a", "b", 1, "x")}}, nil
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	w, err := c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "a", Key: "b"}}, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	err = w.Start(context.Background())
	require.True(t, IsPermissionDenied(err))

	// The watcher can be started again once the problem is fixed.
	denied.Store(false)
	require.NoError(t, w.Start(context.Background()))
	w.Stop()
	select {
	case <-w.Done():
	default:
		t.Fatal("Stop must wait for the goroutine")
	}

	// Client.Close stops running watchers.
	w2, err := c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "a", Key: "b"}}, Timeout: time.Second})
	require.NoError(t, err)
	require.NoError(t, w2.Start(context.Background()))
	require.NoError(t, c.Close(context.Background()))
	select {
	case <-w2.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("client close did not stop the watcher")
	}
}

func TestConfigWatcherRetriesAndGuards(t *testing.T) {
	f := newFakeNode()
	var polls atomic.Int32
	f.watchConfig = func(context.Context, *WatchConfigRequest) (*WatchConfigResponse, error) {
		switch polls.Add(1) {
		case 1:
			return nil, apiError(connect.CodeUnavailable, "", 0, "down")
		case 2:
			return nil, apiError(connect.CodeInvalidArgument, ReasonInvalidArgument, 0, "bad")
		case 3:
			return &WatchConfigResponse{Items: []*ConfigItem{
				testConfigItem("a", "b", 2, "new"),
				testConfigItem("other", "item", 9, "ignored"),
			}}, nil
		default:
			// Misbehaving proxy: immediate empty answers.
			return &WatchConfigResponse{}, nil
		}
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	var panics atomic.Int32
	w, err := c.NewConfigWatcher(WatcherOptions{
		Items:          []ConfigKey{{Group: "a", Key: "b"}},
		Timeout:        50 * time.Millisecond,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		OnChange: func(*ConfigItem) {
			panics.Add(1)
			panic("listener bug")
		},
	})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	eventually(t, 2*time.Second, func() bool {
		it, _ := w.Get("a", "b")
		return it.GetVersion() == 2
	}, "change after failed polls")
	_, ok := w.Get("other", "item")
	require.False(t, ok, "unwatched items are ignored")
	time.Sleep(100 * time.Millisecond)
	require.Less(t, polls.Load(), int32(10), "empty immediate answers are rate limited")
	require.Equal(t, int32(2), panics.Load(), "listener panics are recovered")
	w.Stop()
}

func TestConfigWatcherStopFromListenerAndBeforeStart(t *testing.T) {
	f := newFakeNode()
	f.watchConfig = func(context.Context, *WatchConfigRequest) (*WatchConfigResponse, error) {
		return &WatchConfigResponse{Items: []*ConfigItem{testConfigItem("a", "b", 5, "changed")}}, nil
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)

	var w *ConfigWatcher
	w, err := c.NewConfigWatcher(WatcherOptions{
		Items:   []ConfigKey{{Group: "a", Key: "b"}},
		Timeout: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	stopped := make(chan struct{})
	w.AddListener(func(item *ConfigItem) {
		if item.GetVersion() == 5 {
			w.Stop() // must not deadlock
			close(stopped)
		}
	})
	require.NoError(t, w.Start(context.Background()))
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not run")
	}
	<-w.Done()

	idle, err := c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "a", Key: "b"}}})
	require.NoError(t, err)
	idle.Stop()
	<-idle.Done()
	require.Error(t, idle.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	waiter, err := c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "a", Key: "b"}}})
	require.NoError(t, err)
	_, err = waiter.WaitForChange(ctx, "a", "")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	waiter.Stop()
}

func TestConfigWatcherSecretsNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	f := newFakeNode()
	secretRef := testConfigItem("crawler", "signing.json", 1, `{"key":"resolved-secret"}`)
	secretRef.HasSecretRefs = true
	unresolved := testConfigItem("crawler", "unresolved.json", 1, `{"key":"${secret:signing/api_key}"}`)
	predicate := testConfigItem("signing", "direct.json", 1, `{"key":"inline"}`)
	panicky := testConfigItem("panic", "item.json", 1, `{}`)
	plain := testConfigItem("crawler", "plain.json", 1, `{}`)
	store := newConfigServer(secretRef, unresolved, predicate, panicky, plain)
	store.install(f)
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)

	// Older plain snapshots of the secret item must be removed.
	seed := newSnapshotStore(dir, c.s.host, "ns1", nil, discardLogger())
	old := testConfigItem("crawler", "signing.json", 0, `{"key":"old"}`)
	written, err := seed.save(old)
	require.NoError(t, err)
	require.True(t, written)

	w, err := c.NewConfigWatcher(WatcherOptions{
		Namespace: "ns1",
		Items: []ConfigKey{
			{Group: "crawler", Key: "signing.json"}, {Group: "crawler", Key: "unresolved.json"},
			{Group: "signing", Key: "direct.json"}, {Group: "panic", Key: "item.json"}, {Group: "crawler", Key: "plain.json"},
		},
		Timeout:     50 * time.Millisecond,
		SnapshotDir: dir,
		TreatAsSecret: func(item *ConfigItem) bool {
			if item.GetGroup() == "panic" {
				panic("predicate bug")
			}
			return item.GetGroup() == "signing"
		},
	})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	defer w.Stop()

	var files []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			files = append(files, filepath.Base(path))
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.NotContains(t, string(data), "resolved-secret")
			require.NotContains(t, string(data), "${secret:")
			require.NotContains(t, string(data), "inline")
		}
		return nil
	}))
	require.Equal(t, []string{"plain.json.json"}, files)
	require.Len(t, w.Items(), 5, "secret items are still served from memory")
}

func TestWatcherOptionsValidation(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", nil)
	many := make([]ConfigKey, MaxConfigItems+1)
	for i := range many {
		many[i] = ConfigKey{Group: "g", Key: string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('0'+i/260))}
	}
	tests := []WatcherOptions{
		{},
		{Items: many},
		{Items: []ConfigKey{{Group: "", Key: "k"}}},
		{Items: []ConfigKey{{Group: "g", Key: ""}}},
		{Items: []ConfigKey{{Group: "g", Key: "k"}}, Timeout: 61 * time.Second},
		{Items: []ConfigKey{{Group: "g", Key: "k"}}, Timeout: time.Microsecond},
		{Items: []ConfigKey{{Group: "g", Key: "k"}}, Namespace: string(make([]byte, 65))},
		{Items: []ConfigKey{{Group: "g", Key: "k"}}, InitialBackoff: -1},
	}
	for i, opts := range tests {
		_, err := c.NewConfigWatcher(opts)
		require.Error(t, err, "case %d", i)
	}

	k, err := ParseConfigKey("crawler/search/web.json")
	require.NoError(t, err)
	require.Equal(t, ConfigKey{Group: "crawler", Key: "search/web.json"}, k)
	require.Equal(t, "crawler/search/web.json", k.String())
	_, err = ParseConfigKey("nogroup")
	require.Error(t, err)
	_, err = ParseConfigKey("/key")
	require.Error(t, err)
}

func TestConfigWatcherUsesWatchProcedure(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	w, err := c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "g", Key: "k"}}, Timeout: 20 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	eventually(t, 2*time.Second, func() bool {
		return f.callCount(spinneretv1connect.ConfigServiceWatchConfigProcedure) >= 2
	}, "long polls")
	w.Stop()
	require.Equal(t, "Bearer "+testToken, f.lastHeader(spinneretv1connect.ConfigServiceWatchConfigProcedure).Get("Authorization"))
}
