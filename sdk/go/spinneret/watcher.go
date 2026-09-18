package spinneret

import (
	"context"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
)

// Default config watcher tuning.
const (
	DefaultWatchTimeout      = 30 * time.Second
	DefaultWatchBackoff      = time.Second
	DefaultWatchMaxBackoff   = 30 * time.Second
	watchMinPollInterval     = 200 * time.Millisecond
	maxConfigGroupLength     = 128
	maxConfigKeyLength       = 256
	maxConfigNamespaceLength = 64
)

// ConfigKey addresses a config item inside the namespace.
type ConfigKey struct {
	Group string
	Key   string
}

// ParseConfigKey parses "group/key" (the group ends at the first slash).
func ParseConfigKey(s string) (ConfigKey, error) {
	group, key, ok := strings.Cut(s, "/")
	k := ConfigKey{Group: group, Key: key}
	if !ok {
		return k, newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "config key %q must look like group/key", s)
	}
	return k, k.validate()
}

// String returns "group/key".
func (k ConfigKey) String() string { return k.Group + "/" + k.Key }

func (k ConfigKey) validate() error {
	if k.Group == "" || len([]rune(k.Group)) > maxConfigGroupLength {
		return newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "config group must be 1..%d characters", maxConfigGroupLength)
	}
	if k.Key == "" || len([]rune(k.Key)) > maxConfigKeyLength {
		return newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "config key must be 1..%d characters", maxConfigKeyLength)
	}
	return nil
}

// WatcherOptions configures a [ConfigWatcher].
type WatcherOptions struct {
	// Items are the watched items (1..200, duplicates are ignored).
	Items []ConfigKey
	// Namespace is the namespace name; empty means the token namespace.
	Namespace string
	// Timeout is the long-poll wait sent to the server (default 30s, max 60s).
	Timeout time.Duration
	// SnapshotDir enables local snapshots under this directory: fetched items
	// are written atomically and loaded when the server is unavailable at
	// start. Items with has_secret_refs, content with an unresolved
	// "${secret:" reference and items matched by TreatAsSecret are never
	// written (older snapshots of them are removed). Empty disables snapshots.
	SnapshotDir string
	// TreatAsSecret marks additional items as secret, e.g. items embedding
	// sensitive values directly. It cannot exempt items flagged by the server;
	// a predicate that panics counts as true.
	TreatAsSecret func(*ConfigItem) bool
	// OnChange is registered as the first change listener.
	OnChange func(*ConfigItem)
	// InitialBackoff is the first delay after a failed poll (default 1s).
	InitialBackoff time.Duration
	// MaxBackoff caps the delay between failed polls (default 30s).
	MaxBackoff time.Duration
}

// ConfigWatcher keeps a set of config items up to date with
// ConfigService/WatchConfig.
//
// Start loads the items with BatchGetConfig (falling back to local snapshots
// when the server is unavailable) and starts a goroutine that long-polls for
// changes until the Start context ends, Stop is called or the client is
// closed. Every change is stored, written to the snapshot directory and passed
// to the listeners: on the goroutine calling Start for the initial values, on
// the watch goroutine afterwards. Listeners run sequentially, must not block
// for long and must treat items as read-only; a panicking listener is logged
// and does not stop the watcher.
type ConfigWatcher struct {
	client  *Client
	opts    WatcherOptions
	keys    []ConfigKey
	watched map[ConfigKey]struct{}
	store   *snapshotStore
	logger  *slog.Logger
	rnd     func() float64

	dispatching atomic.Bool

	mu           sync.Mutex
	items        map[ConfigKey]*ConfigItem
	changedSeq   map[ConfigKey]uint64
	seq          uint64
	listeners    []func(*ConfigItem)
	fromSnapshot bool
	started      bool
	stopped      bool
	changed      chan struct{}
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewConfigWatcher creates a watcher bound to the client (not started).
func (c *Client) NewConfigWatcher(opts WatcherOptions) (*ConfigWatcher, error) {
	if c.closed.Load() {
		return nil, newError(connect.CodeFailedPrecondition, ReasonClientClosed, "the client has been closed")
	}
	w := &ConfigWatcher{
		client:     c,
		watched:    make(map[ConfigKey]struct{}),
		logger:     c.s.logger,
		rnd:        c.rnd,
		items:      make(map[ConfigKey]*ConfigItem),
		changedSeq: make(map[ConfigKey]uint64),
		changed:    make(chan struct{}),
		done:       make(chan struct{}),
	}
	if err := w.configure(opts); err != nil {
		return nil, err
	}
	if opts.SnapshotDir != "" {
		w.store = newSnapshotStore(opts.SnapshotDir, c.s.host, opts.Namespace, opts.TreatAsSecret, c.s.logger)
	}
	if opts.OnChange != nil {
		w.listeners = []func(*ConfigItem){opts.OnChange}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, newError(connect.CodeFailedPrecondition, ReasonClientClosed, "the client has been closed")
	}
	c.watchers[w] = struct{}{}
	return w, nil
}

func (w *ConfigWatcher) configure(opts WatcherOptions) error {
	for _, k := range opts.Items {
		if err := k.validate(); err != nil {
			return err
		}
		if _, dup := w.watched[k]; !dup {
			w.watched[k] = struct{}{}
			w.keys = append(w.keys, k)
		}
	}
	if len(w.keys) == 0 || len(w.keys) > MaxConfigItems {
		return errInvalidOptions("a config watcher needs 1..%d items", MaxConfigItems)
	}
	if len([]rune(opts.Namespace)) > maxConfigNamespaceLength {
		return errInvalidOptions("namespace must be at most %d characters", maxConfigNamespaceLength)
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultWatchTimeout
	}
	if opts.Timeout < time.Millisecond || opts.Timeout > MaxWatchTimeoutMs*time.Millisecond {
		return errInvalidOptions("watch timeout must be within 1ms..60s")
	}
	if opts.InitialBackoff < 0 || opts.MaxBackoff < 0 {
		return errInvalidOptions("watch backoff must not be negative")
	}
	if opts.InitialBackoff == 0 {
		opts.InitialBackoff = DefaultWatchBackoff
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = DefaultWatchMaxBackoff
	}
	opts.MaxBackoff = max(opts.MaxBackoff, opts.InitialBackoff)
	w.opts = opts
	return nil
}

// Keys returns the watched keys in declaration order.
func (w *ConfigWatcher) Keys() []ConfigKey { return slices.Clone(w.keys) }

// Get returns the latest known item.
func (w *ConfigWatcher) Get(group, key string) (*ConfigItem, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	item, ok := w.items[ConfigKey{Group: group, Key: key}]
	return item, ok
}

// Items returns a copy of the map of known items.
func (w *ConfigWatcher) Items() map[ConfigKey]*ConfigItem {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.items)
}

// Versions returns the known version of every item (items never published are absent).
func (w *ConfigWatcher) Versions() map[ConfigKey]int32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[ConfigKey]int32, len(w.items))
	for k, item := range w.items {
		out[k] = item.GetVersion()
	}
	return out
}

// FromSnapshot reports whether the items come from local snapshots and the
// server has not answered yet.
func (w *ConfigWatcher) FromSnapshot() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fromSnapshot
}

// AddListener registers a callback invoked with every changed item.
func (w *ConfigWatcher) AddListener(fn func(*ConfigItem)) {
	if fn == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listeners = append(slices.Clip(w.listeners), fn)
}

// Done returns a channel closed when the watch goroutine has exited.
func (w *ConfigWatcher) Done() <-chan struct{} { return w.done }

// Start loads the items and starts the watch goroutine, which runs until ctx
// ends, Stop is called or the client is closed.
//
// When the server is unavailable ([IsRetryable] errors) the snapshots are
// loaded instead and FromSnapshot reports true until the server answers.
// Other errors (for example permission_denied) are returned and the watcher
// can be started again.
func (w *ConfigWatcher) Start(ctx context.Context) error {
	w.mu.Lock()
	switch {
	case w.stopped:
		w.mu.Unlock()
		return newError(connect.CodeFailedPrecondition, ReasonFailedPrecondition, "a stopped config watcher cannot be restarted")
	case w.started:
		w.mu.Unlock()
		return newError(connect.CodeFailedPrecondition, ReasonFailedPrecondition, "config watcher already started")
	}
	w.started = true
	w.mu.Unlock()
	if err := w.initialLoad(ctx); err != nil {
		w.mu.Lock()
		w.started = false
		if w.stopped {
			// Stop ran during the load and left closing Done to Start.
			close(w.done)
		}
		w.mu.Unlock()
		return err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		cancel()
		close(w.done)
		return newError(connect.CodeFailedPrecondition, ReasonFailedPrecondition, "config watcher stopped during start")
	}
	w.cancel = cancel
	w.mu.Unlock()
	go w.run(loopCtx)
	return nil
}

// Stop ends the watch loop, cancelling an in-flight long poll, and waits for
// the goroutine to exit (unless called from a listener). Stop is idempotent.
func (w *ConfigWatcher) Stop() {
	w.mu.Lock()
	alreadyStopped := w.stopped
	w.stopped = true
	cancel := w.cancel
	started := w.started
	w.broadcastLocked()
	w.mu.Unlock()
	if !alreadyStopped {
		w.client.forgetWatcher(w)
	}
	if cancel == nil {
		// Not running: close Done unless a Start in progress will do it.
		if !alreadyStopped && !started {
			close(w.done)
		}
		return
	}
	cancel()
	if !w.dispatching.Load() {
		<-w.done
	}
}

// WaitForChange blocks until an item changes after the call and returns it.
// Empty group or key match any value. It fails when ctx ends or the watcher
// stops.
func (w *ConfigWatcher) WaitForChange(ctx context.Context, group, key string) (*ConfigItem, error) {
	w.mu.Lock()
	start := w.seq
	for {
		if item := w.changedSinceLocked(start, group, key); item != nil {
			w.mu.Unlock()
			return item, nil
		}
		if w.stopped {
			w.mu.Unlock()
			return nil, newError(connect.CodeFailedPrecondition, ReasonFailedPrecondition, "config watcher stopped")
		}
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		w.mu.Lock()
	}
}

func (w *ConfigWatcher) changedSinceLocked(start uint64, group, key string) *ConfigItem {
	var (
		best    ConfigKey
		bestSeq uint64
	)
	for k, seq := range w.changedSeq {
		if seq <= start || (group != "" && k.Group != group) || (key != "" && k.Key != key) {
			continue
		}
		if bestSeq == 0 || seq < bestSeq {
			best, bestSeq = k, seq
		}
	}
	if bestSeq == 0 {
		return nil
	}
	return w.items[best]
}

func (w *ConfigWatcher) broadcastLocked() {
	close(w.changed)
	w.changed = make(chan struct{})
}

func (w *ConfigWatcher) refs() []*ConfigRef {
	refs := make([]*ConfigRef, len(w.keys))
	for i, k := range w.keys {
		refs[i] = &ConfigRef{Group: k.Group, Key: k.Key}
	}
	return refs
}

func (w *ConfigWatcher) initialLoad(ctx context.Context) error {
	resp, err := w.client.BatchGetConfig(ctx, &BatchGetConfigRequest{Namespace: w.opts.Namespace, Items: w.refs()})
	if err != nil {
		if !IsRetryable(err) || ctx.Err() != nil {
			return err
		}
		w.logger.WarnContext(ctx, "spinneret config server unavailable, using local snapshots",
			slog.String("error", err.Error()))
		w.apply(ctx, w.loadSnapshots(), false, true)
		return nil
	}
	if missing := resp.GetMissing(); len(missing) > 0 {
		names := make([]string, len(missing))
		for i, m := range missing {
			names[i] = m.GetGroup() + "/" + m.GetKey()
		}
		w.logger.InfoContext(ctx, "spinneret config items not found", slog.String("items", strings.Join(names, ", ")))
	}
	w.apply(ctx, resp.GetItems(), true, false)
	return nil
}

func (w *ConfigWatcher) loadSnapshots() []*ConfigItem {
	if w.store == nil {
		return nil
	}
	var items []*ConfigItem
	for _, k := range w.keys {
		item, err := w.store.load(k.Group, k.Key)
		if err != nil {
			w.logger.Warn("spinneret ignored config snapshot",
				slog.String("item", k.String()), slog.String("error", err.Error()))
			continue
		}
		if item != nil {
			items = append(items, item)
		}
	}
	return items
}

func (w *ConfigWatcher) watchItems() []*WatchItem {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*WatchItem, len(w.keys))
	for i, k := range w.keys {
		out[i] = &WatchItem{Group: k.Group, Key: k.Key, Version: max(w.items[k].GetVersion(), 0)}
	}
	return out
}

func (w *ConfigWatcher) run(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.stopped = true
		w.broadcastLocked()
		w.mu.Unlock()
		w.client.forgetWatcher(w)
		close(w.done)
	}()
	bo := backoff{initial: w.opts.InitialBackoff, max: w.opts.MaxBackoff}
	failures := 0
	timeoutMs := int32(w.opts.Timeout.Milliseconds())
	for ctx.Err() == nil {
		started := time.Now()
		resp, err := w.client.watchOnce(ctx, &WatchConfigRequest{
			Namespace: w.opts.Namespace,
			Items:     w.watchItems(),
			TimeoutMs: timeoutMs,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ReasonOf(err) == ReasonClientClosed {
				w.logger.Info("spinneret config watcher stopped: the client has been closed")
				return
			}
			delay := bo.delay(failures, w.rnd)
			failures++
			level := slog.LevelWarn
			if !IsRetryable(err) {
				level = slog.LevelError
			}
			w.logger.Log(ctx, level, "spinneret config watch failed, retrying",
				slog.Duration("delay", delay), slog.String("error", err.Error()))
			if !sleepContext(ctx, delay) {
				return
			}
			continue
		}
		failures = 0
		w.apply(ctx, resp.GetItems(), true, false)
		if len(resp.GetItems()) == 0 && time.Since(started) < watchMinPollInterval {
			// A poll without changes must wait for the timeout; guard against
			// a misbehaving proxy answering immediately.
			if !sleepContext(ctx, watchMinPollInterval) {
				return
			}
		}
	}
}

// apply stores the items that differ from the known ones, persists them and
// notifies the listeners.
func (w *ConfigWatcher) apply(ctx context.Context, items []*ConfigItem, persist, fromSnapshot bool) {
	w.mu.Lock()
	var changed []*ConfigItem
	for _, item := range items {
		k := ConfigKey{Group: item.GetGroup(), Key: item.GetKey()}
		if _, ok := w.watched[k]; !ok {
			continue
		}
		if cur, ok := w.items[k]; ok && cur.GetVersion() == item.GetVersion() && cur.GetContent() == item.GetContent() {
			continue
		}
		w.items[k] = item
		w.seq++
		w.changedSeq[k] = w.seq
		changed = append(changed, item)
	}
	w.fromSnapshot = fromSnapshot
	listeners := slices.Clone(w.listeners)
	w.broadcastLocked()
	w.mu.Unlock()
	if persist && w.store != nil {
		for _, item := range changed {
			if _, err := w.store.save(item); err != nil {
				w.logger.WarnContext(ctx, "spinneret could not write config snapshot",
					slog.String("group", item.GetGroup()), slog.String("key", item.GetKey()),
					slog.String("error", err.Error()))
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	w.dispatching.Store(true)
	defer w.dispatching.Store(false)
	for _, item := range changed {
		for _, fn := range listeners {
			w.notify(ctx, fn, item)
		}
	}
}

func (w *ConfigWatcher) notify(ctx context.Context, fn func(*ConfigItem), item *ConfigItem) {
	defer func() {
		if p := recover(); p != nil {
			w.logger.ErrorContext(ctx, "spinneret config change listener panicked",
				slog.String("group", item.GetGroup()), slog.String("key", item.GetKey()),
				slog.Any("panic", p), slog.String("stack", string(debug.Stack())))
		}
	}()
	fn(item)
}
