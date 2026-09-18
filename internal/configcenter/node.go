package configcenter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// noHeldVersion marks targets of plain reads (no version held by the caller).
const noHeldVersion int32 = -1

// target is a published item selected for delivery.
type target struct {
	key itemKey
	cur itemVersion
	// held is the version a watcher holds (noHeldVersion for plain reads).
	// Items that resolve to exactly that version are not changes and are
	// skipped.
	held int32
}

// GetConfig returns one published item with secret references resolved. It
// requires config:read on the item's group; the reserved "_runtime" group is
// served when the principal can read that group. Items that do not exist or
// were never published are not_found.
func (s *Service) GetConfig(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ref Ref) (NodeItem, error) {
	items, _, err := s.BatchGetConfig(ctx, p, ns, []Ref{ref})
	if err != nil {
		return NodeItem{}, err
	}
	if len(items) == 0 {
		return NodeItem{}, apperr.NotFound("config item %q not found or not published", itemName(ref.Group, ref.Key))
	}
	return items[0], nil
}

// BatchGetConfig returns published items in request order (duplicates are
// served once) and lists unknown or unpublished items in missing. The whole
// request fails when the principal lacks config:read on any requested group
// or read access to any referenced secret (permission_denied, audited), or
// when the combined content exceeds Config.MaxResponseBytes.
func (s *Service) BatchGetConfig(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, refs []Ref) ([]NodeItem, []Ref, error) {
	if err := requireCaller(p, ns); err != nil {
		return nil, nil, err
	}
	if len(refs) == 0 || len(refs) > MaxNodeItems {
		return nil, nil, apperr.InvalidArgument("", "between 1 and %d items are required", MaxNodeItems)
	}
	refs = dedupeRefs(refs)
	for _, r := range refs {
		if err := requireNodeRead(p, ns, r.Group); err != nil {
			return nil, nil, err
		}
	}
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	keys := make([]itemKey, len(refs))
	for i, r := range refs {
		keys[i] = itemKey{ns: ns.ID, group: r.Group, key: r.Key}
	}
	versions, err := s.loadVersions(ctx, keys)
	if err != nil {
		return nil, nil, err
	}
	targets := make([]target, 0, len(keys))
	for _, k := range keys {
		if v := versions[k]; v.version > 0 {
			targets = append(targets, target{key: k, cur: v, held: noHeldVersion})
		}
	}
	items, err := s.resolve(ctx, p, ns, targets, false)
	if err != nil {
		return nil, nil, err
	}
	delivered := make(map[itemKey]struct{}, len(items))
	for _, it := range items {
		delivered[itemKey{ns: ns.ID, group: it.Group, key: it.Key}] = struct{}{}
	}
	var missing []Ref
	for i, k := range keys {
		if _, ok := delivered[k]; !ok {
			missing = append(missing, refs[i])
		}
	}
	return items, missing, nil
}

// WatchConfig long-polls for changes of the watched items. It returns at
// once the items whose published version differs from the held version, or
// blocks until such a change, ctx cancellation or the timeout (0 selects the
// default, values above Config.MaxWatchTimeout are capped; a ctx deadline
// shortens it) and then returns an empty list.
//
// Semantics: an item counts as changed when it has a published version that
// differs from the held one (so a held version 0 returns the item as soon as
// it is published, and an item deleted and created again is returned with
// its new version). Deleted or unpublished items are never returned and do
// not end the poll. When the caller holds a newer version than this
// instance's cached one (read through a peer instance that already processed
// a publish, or after a lost bus event) the item is re-read from the source
// of truth first, so a node is never moved back to an older version nor sent
// the version it already holds. When the changed items exceed
// Config.MaxResponseBytes a subset is returned; the rest are returned by the
// next poll.
//
// Blocked calls count against Config.MaxWatchers (resource_exhausted /
// rate_limited beyond it). Permissions are those of BatchGetConfig.
func (s *Service) WatchConfig(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, items []WatchRef, timeout time.Duration) ([]NodeItem, error) {
	if err := requireCaller(p, ns); err != nil {
		return nil, err
	}
	keys, held, err := s.watchKeys(p, ns, items)
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(s.watchTimeout(ctx, timeout))
	defer timer.Stop()

	w := newWaiter(keys, held)
	defer s.hub.unregister(w)
	spins := 0
	for {
		snap, seq, err := s.snapshot(ctx, keys)
		if err != nil {
			return nil, err
		}
		changes, err := s.hub.register(w, snap, seq, spins >= maxWatchSpins)
		if err != nil {
			return nil, err
		}
		if len(changes) > 0 && spins < maxWatchSpins {
			out, err := s.deliverChanges(ctx, p, ns, keys, held, changes)
			if err != nil {
				return nil, err
			}
			if len(out) > 0 {
				return out, nil
			}
			// The cache disagreed with the source of truth (it was corrected
			// or the keys were marked for re-verification); check again a
			// bounded number of times.
			spins++
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return []NodeItem{}, nil
		case <-w.wake:
			spins = 0
		}
	}
}

func (s *Service) snapshot(ctx context.Context, keys []itemKey) ([]itemVersion, uint64, error) {
	lctx, cancel := s.opContext(ctx)
	defer cancel()
	return s.hub.snapshot(lctx, keys)
}

// watchKeys validates the watched items and permissions.
func (s *Service) watchKeys(p *authz.Principal, ns *catalog.Namespace, items []WatchRef) ([]itemKey, []int32, error) {
	if len(items) == 0 || len(items) > MaxNodeItems {
		return nil, nil, apperr.InvalidArgument("", "between 1 and %d items are required", MaxNodeItems)
	}
	keys := make([]itemKey, len(items))
	held := make([]int32, len(items))
	seen := make(map[Ref]struct{}, len(items))
	for i, it := range items {
		ref := Ref{Group: it.Group, Key: it.Key}
		if _, dup := seen[ref]; dup {
			return nil, nil, apperr.InvalidArgument("", "duplicate watch item %q", itemName(it.Group, it.Key))
		}
		seen[ref] = struct{}{}
		if it.Version < 0 {
			return nil, nil, apperr.InvalidArgument("", "watch item %q: version must not be negative", itemName(it.Group, it.Key))
		}
		if err := requireNodeRead(p, ns, it.Group); err != nil {
			return nil, nil, err
		}
		keys[i] = itemKey{ns: ns.ID, group: it.Group, key: it.Key}
		held[i] = it.Version
	}
	return keys, held, nil
}

func (s *Service) watchTimeout(ctx context.Context, requested time.Duration) time.Duration {
	t := requested
	if t <= 0 {
		t = s.cfg.DefaultWatchTimeout
	}
	t = min(t, s.cfg.MaxWatchTimeout)
	if dl, ok := ctx.Deadline(); ok {
		t = max(min(t, time.Until(dl)-deadlineSafetyShift), 0)
	}
	return t
}

// deliverChanges verifies and resolves the changed items of a watch.
func (s *Service) deliverChanges(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, keys []itemKey, held []int32,
	changes []change,
) ([]NodeItem, error) {
	lctx, cancel := s.opContext(ctx)
	defer cancel()
	changes, err := s.verifyChanges(lctx, keys, held, changes)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	return s.resolveChanges(lctx, p, ns, keys, held, changes)
}

// verifyChanges re-reads, from the source of truth, changed items whose
// cached version is older than the version the caller holds. The caller
// learned that version elsewhere (a peer instance that already processed a
// publish, or before a bus event was lost), so the cache may be stale:
// delivering it would move the node back to an older version, and for
// runtime items make it spin on immediate answers. Verified items that turn
// out unchanged or deleted are dropped; the hub keeps the verified versions.
func (s *Service) verifyChanges(ctx context.Context, keys []itemKey, held []int32, changes []change) ([]change, error) {
	var suspect []itemKey
	for _, c := range changes {
		if held[c.index] > c.cur.version {
			suspect = append(suspect, keys[c.index])
		}
	}
	if len(suspect) == 0 {
		return changes, nil
	}
	fresh, err := s.hub.reload(ctx, suspect)
	if err != nil {
		return nil, err
	}
	out := make([]change, 0, len(changes))
	next := 0
	for _, c := range changes {
		if held[c.index] > c.cur.version {
			c.cur = fresh[next]
			next++
			if c.cur.version == 0 || c.cur.version == held[c.index] {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// resolveChanges resolves changed items within the byte budget. When none
// of them can be delivered (their contents vanished or they resolved to the
// held version, so the cached versions are stale) the keys are scheduled for
// re-verification.
func (s *Service) resolveChanges(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, keys []itemKey, held []int32,
	changes []change,
) ([]NodeItem, error) {
	targets := make([]target, len(changes))
	stale := make([]itemKey, len(changes))
	for i, c := range changes {
		targets[i] = target{key: keys[c.index], cur: c.cur, held: held[c.index]}
		stale[i] = keys[c.index]
	}
	out, err := s.resolve(ctx, p, ns, targets, true)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		s.hub.markDirty(stale)
	}
	return out, nil
}

// resolve loads the contents of targets and resolves secret references. With
// partial set, items beyond the byte budget are deferred (at least one item
// is returned when any exists); otherwise exceeding it fails. Targets whose
// content no longer exists are omitted.
func (s *Service) resolve(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, targets []target, partial bool) ([]NodeItem, error) {
	budget := s.cfg.MaxResponseBytes
	var ckeys []contentKey
	for _, t := range targets {
		if t.key.group != RuntimeGroup {
			ckeys = append(ckeys, contentKey{itemID: t.cur.id, version: t.cur.version})
		}
	}
	sel, err := s.loadContents(ctx, ckeys, budget, partial)
	if err != nil {
		return nil, err
	}
	secretValues := make(map[SecretRef]string)
	out := make([]NodeItem, 0, len(targets))
	var total int64
	for _, t := range targets {
		item, ok, err := s.resolveTarget(ctx, p, ns, t, sel, secretValues)
		if err != nil {
			return nil, err
		}
		if !ok || item.Version == t.held {
			continue
		}
		size := int64(len(item.Content))
		if len(out) > 0 && total+size > budget {
			if !partial {
				return nil, tooLargeError(budget)
			}
			break
		}
		total += size
		out = append(out, item)
	}
	return out, nil
}

// resolveTarget builds the node item of one target; ok is false when its
// content is absent or deferred by the budget.
func (s *Service) resolveTarget(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, t target, sel contentSelection,
	secretValues map[SecretRef]string,
) (NodeItem, bool, error) {
	item := NodeItem{Namespace: ns.Name, Group: t.key.group, Key: t.key.key}
	if t.key.group == RuntimeGroup {
		rc, ok, err := s.runtimeContentFor(ctx, ns.ID, t.key.key, t.cur.version)
		if err != nil || !ok {
			return NodeItem{}, false, err
		}
		item.Format, item.Version, item.Content = FormatJSON, rc.version, rc.body
		return item, true, nil
	}
	c, ok := sel.found[contentKey{itemID: t.cur.id, version: t.cur.version}]
	if !ok {
		return NodeItem{}, false, nil
	}
	if c.refsErr != nil {
		return NodeItem{}, false, fmt.Errorf("config item %s version %d has invalid secret references: %w", t.cur.id, t.cur.version, c.refsErr)
	}
	published := c.publishedAt
	item.Format, item.Version, item.UpdatedAt = c.format, t.cur.version, &published
	if len(c.refs) == 0 {
		item.Content = c.body
		return item, true, nil
	}
	for _, ref := range c.refs {
		if _, done := secretValues[ref]; done {
			continue
		}
		v, err := s.readSecret(ctx, p, ns, t, ref)
		if err != nil {
			return NodeItem{}, false, err
		}
		secretValues[ref] = v
	}
	body, err := substituteSecretRefs(c.format, c.body, c.spans, secretValues)
	if err != nil {
		return NodeItem{}, false, err
	}
	item.Content, item.HasSecretRefs = body, true
	return item, true, nil
}

// readSecret resolves one secret reference for a node read. Permission
// failures are audited and fail the whole request.
func (s *Service) readSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, t target, ref SecretRef) (string, error) {
	name := itemName(t.key.group, t.key.key)
	if s.secrets == nil {
		return "", apperr.FailedPrecondition("", "config item %q references secrets but secret resolution is not available", name)
	}
	sv, err := s.secrets.ReadSecret(ctx, p, ns, ref.Path, ref.Version, "config:"+name)
	if err == nil {
		return sv.Value, nil
	}
	ae, isApp := apperr.As(err)
	switch {
	case isApp && ae.Code == connect.CodePermissionDenied:
		s.record(ctx, p, ns, ActionRead, t.cur.id, t.key.group, t.key.key, audit.ResultDenied,
			map[string]any{"secret_path": ref.Path, "secret_version": ref.Version})
		return "", apperr.PermissionDenied(ae.Reason, "reading config item %q requires read access to secret %q", name, ref.String())
	case isApp && ae.Code == connect.CodeNotFound:
		return "", apperr.FailedPrecondition("", "config item %q references secret %q which does not exist", name, ref.String())
	case isApp, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", err
	default:
		return "", fmt.Errorf("read secret for config item %s: %w", name, err)
	}
}

// requireNodeRead checks config:read on a group for node reads.
func requireNodeRead(p *authz.Principal, ns *catalog.Namespace, group string) error {
	return p.Require(authz.PermConfigRead, configResource(ns, group))
}

func dedupeRefs(refs []Ref) []Ref {
	seen := make(map[Ref]struct{}, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
