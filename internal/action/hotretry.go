package action

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/TikHub/Spinneret/internal/action/actiondb"
)

// Hot-state retry settings of the expiry job.
const (
	// maxHotRetryEntries bounds the subjects kept for a retry; beyond it the
	// next pass reconciles recent releases from state_events instead.
	maxHotRetryEntries = 100_000
	// expiryCatchUpWindow is how far back a pass re-synchronizes expiry
	// releases when this instance did not run the previous passes (first pass
	// after becoming leader, or after the retry set overflowed).
	expiryCatchUpWindow = 10 * time.Minute
	// expiryCatchUpRows bounds the releases one catch-up re-synchronizes.
	expiryCatchUpRows = 50_000
	// hotSyncChunk bounds the IDs passed to one hot-state synchronization.
	hotSyncChunk = 1000
)

// hotRetry is the set of subjects whose hot-state push failed after their
// PostgreSQL release was committed. The expiry job re-synchronizes them from
// PostgreSQL at the start of every pass until it succeeds. It is safe for
// concurrent use.
type hotRetry struct {
	mu         sync.Mutex
	limit      int
	size       int
	identities map[string]map[string]struct{} // site ID -> identity IDs
	accounts   map[string]map[string]struct{} // site ID -> account IDs
	proxies    map[string]map[string]struct{} // namespace ID -> proxy IDs
	// overflowAt is when an entry was rejected because the set was full (zero
	// when nothing was lost).
	overflowAt time.Time
}

func newHotRetry(limit int) *hotRetry {
	return &hotRetry{
		limit:      limit,
		identities: map[string]map[string]struct{}{},
		accounts:   map[string]map[string]struct{}{},
		proxies:    map[string]map[string]struct{}{},
	}
}

// add records subjects of kind (SubjectIdentity, SubjectAccount or
// SubjectProxy) in scope (site or namespace ID).
func (r *hotRetry) add(kind, scope string, ids []string, now time.Time) {
	if len(ids) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.byKind(kind)
	set := m[scope]
	if set == nil {
		set = map[string]struct{}{}
		m[scope] = set
	}
	for _, id := range ids {
		if _, ok := set[id]; ok {
			continue
		}
		if r.size >= r.limit {
			if r.overflowAt.IsZero() {
				r.overflowAt = now
			}
			continue
		}
		set[id] = struct{}{}
		r.size++
	}
}

// remove forgets subjects that were synchronized.
func (r *hotRetry) remove(kind, scope string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.byKind(kind)
	set := m[scope]
	for _, id := range ids {
		if _, ok := set[id]; ok {
			delete(set, id)
			r.size--
		}
	}
	if len(set) == 0 {
		delete(m, scope)
	}
}

// snapshot returns the recorded subjects of kind by scope (sorted copies).
func (r *hotRetry) snapshot(kind string) map[string][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]string{}
	for scope, set := range r.byKind(kind) {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out[scope] = ids
	}
	return out
}

// takeOverflow returns and clears the time of the first rejected entry.
func (r *hotRetry) takeOverflow() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.overflowAt
	r.overflowAt = time.Time{}
	return at
}

// len returns the number of recorded subjects.
func (r *hotRetry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// byKind returns the map of kind; the caller holds mu.
func (r *hotRetry) byKind(kind string) map[string]map[string]struct{} {
	switch kind {
	case SubjectAccount:
		return r.accounts
	case SubjectProxy:
		return r.proxies
	default:
		return r.identities
	}
}

// retryHotPushes re-synchronizes the subjects of failed pushes from
// PostgreSQL (the authoritative synchronization applies the committed
// release, which is newer than the Redis state it replaces). Subjects that
// synchronized are forgotten; the others stay for the next pass.
func (o *Operator) retryHotPushes(ctx context.Context) error {
	if o.hot == nil {
		return nil
	}
	var errs []error
	for _, kind := range []string{SubjectAccount, SubjectIdentity, SubjectProxy} {
		for scope, ids := range o.retry.snapshot(kind) {
			for start := 0; start < len(ids); start += hotSyncChunk {
				chunk := ids[start:min(start+hotSyncChunk, len(ids))]
				if err := o.syncKind(ctx, kind, scope, chunk); err != nil {
					errs = append(errs, err)
					continue
				}
				o.retry.remove(kind, scope, chunk)
			}
		}
	}
	return errors.Join(errs...)
}

// syncKind runs the hot syncer of one subject kind.
func (o *Operator) syncKind(ctx context.Context, kind, scope string, ids []string) error {
	switch kind {
	case SubjectAccount:
		return o.syncAccounts(ctx, scope, ids)
	case SubjectProxy:
		return o.syncProxies(ctx, scope, ids)
	default:
		return o.syncIdentities(ctx, scope, ids, SyncOptions{})
	}
}

// catchUpExpiry re-synchronizes the subjects released by the expiry job since
// since (at most expiryCatchUpRows), recording the ones that fail for retry.
// It covers pushes that failed on a previous leader or were not recorded
// because the retry set was full.
func (o *Operator) catchUpExpiry(ctx context.Context, since, now time.Time) error {
	if o.hot == nil {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	rows, err := actiondb.New(o.pool).ActionRecentExpiryReleases(qctx, actiondb.ActionRecentExpiryReleasesParams{
		Since: since, MaxRows: expiryCatchUpRows,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("load recent expiry releases: %w", err)
	}
	type group struct{ kind, scope string }
	groups := map[group][]string{}
	for _, r := range rows {
		scope := r.SiteID
		if r.SubjectKind == SubjectProxy {
			scope = r.NamespaceID
		}
		if scope == "" {
			continue
		}
		k := group{kind: r.SubjectKind, scope: scope}
		groups[k] = append(groups[k], r.SubjectID)
	}
	o.logger.Info("re-synchronizing recent expiry releases", "since", since, "subjects", len(rows))
	var errs []error
	for k, ids := range groups {
		for start := 0; start < len(ids); start += hotSyncChunk {
			chunk := ids[start:min(start+hotSyncChunk, len(ids))]
			if err := o.syncKind(ctx, k.kind, k.scope, chunk); err != nil {
				o.retry.add(k.kind, k.scope, chunk, now)
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// catchUpSince reports whether a pass starting at now must re-synchronize
// recent expiry releases, and since when: on the first pass of this instance,
// after missed passes (leadership was elsewhere) and after the retry set
// overflowed. The window is bounded by expiryCatchUpWindow.
func (o *Operator) catchUpSince(prevPass, now time.Time) (time.Time, bool) {
	floor := now.Add(-expiryCatchUpWindow)
	var since time.Time
	switch {
	case prevPass.IsZero():
		since = floor
	case now.Sub(prevPass) > 3*expiryInterval:
		since = prevPass.Add(-expiryTimeout)
	}
	if at := o.retry.takeOverflow(); !at.IsZero() && (since.IsZero() || at.Before(since)) {
		since = at.Add(-expiryTimeout)
	}
	if since.IsZero() {
		return time.Time{}, false
	}
	if since.Before(floor) {
		since = floor
	}
	return since, true
}
