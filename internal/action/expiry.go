package action

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/action/actiondb"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/site"
)

// Expiry job settings (spec §6.8).
const (
	ExpiryJobName    = "action.expiry"
	expiryInterval   = 10 * time.Second
	expiryTimeout    = 60 * time.Second
	expiryBatch      = 1000
	maxExpiryBatches = 20
)

// ExpiryJob returns the leader job that ends due bans and quarantines.
func (o *Operator) ExpiryJob() jobs.Job {
	return jobs.Job{
		Name:     ExpiryJobName,
		Interval: expiryInterval,
		Mode:     jobs.Leader,
		Timeout:  expiryTimeout,
		Run:      o.RunExpiry,
	}
}

// RunExpiry performs one expiry pass: accounts banned until <= now → active,
// identities banned until <= now → the ban_expiry_state of their site+client
// "_default" action policy (pending or active), identities quarantined until
// <= now → pending, proxies banned or quarantined until <= now → active.
// Each category processes at most maxExpiryBatches × 1000 rows per pass.
//
// Releases are committed to PostgreSQL first and then pushed to Redis. So
// that a release never stays invisible to the scheduler:
//   - the pass releases nothing while Redis does not answer PING (the rows
//     stay due and are released by the first pass after Redis recovers);
//   - subjects whose push fails after the commit are recorded and
//     re-synchronized from PostgreSQL at the start of every following pass
//     until that succeeds;
//   - the first pass of an instance (a new leader), a pass after missed
//     passes and a pass after the retry set overflowed re-synchronize the
//     releases recorded in state_events during the last 10 minutes, covering
//     pushes that failed on the previous leader.
func (o *Operator) RunExpiry(ctx context.Context) error {
	now := o.now()
	if err := o.pingRedis(ctx); err != nil {
		return fmt.Errorf("expiry pass skipped, redis is unavailable: %w", err)
	}
	var prev time.Time
	if ns := o.lastExpiry.Swap(now.UnixNano()); ns != 0 {
		prev = time.Unix(0, ns)
	}
	var errs []error
	if since, ok := o.catchUpSince(prev, now); ok {
		errs = append(errs, o.catchUpExpiry(ctx, since, now))
	}
	if o.retry.len() > 0 {
		errs = append(errs, o.retryHotPushes(ctx))
	}
	errs = append(errs,
		o.expireAccountBans(ctx, now),
		o.expireIdentityBans(ctx, now),
		o.expireQuarantines(ctx, now),
		o.expireProxies(ctx, now),
	)
	return errors.Join(errs...)
}

// pingRedis checks that the hot state answers before releasing anything.
func (o *Operator) pingRedis(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
	defer cancel()
	if err := o.apply.rdb.Do(pctx, o.apply.rdb.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}

// tenancy resolves the namespace/tenant of sites and namespaces for events,
// preferring the catalog and falling back to PostgreSQL.
type tenancy struct {
	siteNS       map[string][2]string // site → {namespace, tenant}
	tenantOfNS   map[string]string
	siteSnapshot map[string]*catalog.Site
}

func (o *Operator) resolveTenancy(ctx context.Context, q *actiondb.Queries, siteIDs, nsIDs []string) (*tenancy, error) {
	t := &tenancy{siteNS: map[string][2]string{}, tenantOfNS: map[string]string{}, siteSnapshot: map[string]*catalog.Site{}}
	var missingSites, missingNS []string
	for _, id := range siteIDs {
		if _, done := t.siteNS[id]; done {
			continue
		}
		if s, ns, ok := o.cat.Site(id); ok {
			t.siteNS[id], t.siteSnapshot[id] = [2]string{ns.ID, ns.TenantID}, s
			continue
		}
		missingSites = append(missingSites, id)
	}
	for _, id := range nsIDs {
		if ns, ok := o.cat.Namespace(id); ok {
			t.tenantOfNS[id] = ns.TenantID
			continue
		}
		missingNS = append(missingNS, id)
	}
	if len(missingSites) > 0 {
		rows, err := q.ActionSiteTenancy(ctx, missingSites)
		if err != nil {
			return nil, fmt.Errorf("resolve site tenancy: %w", err)
		}
		for _, r := range rows {
			t.siteNS[r.ID] = [2]string{r.NamespaceID, r.TenantID}
		}
	}
	if len(missingNS) > 0 {
		rows, err := q.ActionNamespaceTenants(ctx, missingNS)
		if err != nil {
			return nil, fmt.Errorf("resolve namespace tenancy: %w", err)
		}
		for _, r := range rows {
			t.tenantOfNS[r.ID] = r.TenantID
		}
	}
	return t, nil
}

// expiredChange builds the state event of a system expiry transition.
func expiredChange(t *tenancy, now time.Time, siteID, kind, id string, key int64, from, to, action, reason string) StateChange {
	tn := t.siteNS[siteID]
	return StateChange{
		At: now, TenantID: tn[1], NamespaceID: tn[0], SiteID: siteID,
		SubjectKind: kind, SubjectID: id, SubjectKey: key, FromState: from, ToState: to,
		Action: action, Scope: kind, Actor: ActorSystem, Reason: reason,
	}
}

func (o *Operator) expireAccountBans(ctx context.Context, now time.Time) error {
	for batch := 0; batch < maxExpiryBatches; batch++ {
		var rows []actiondb.ActionReleaseDueAccountBansRow
		var changes []StateChange
		var tn *tenancy
		err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
			var err error
			if rows, err = q.ActionReleaseDueAccountBans(ctx, actiondb.ActionReleaseDueAccountBansParams{Now: now, MaxRows: expiryBatch}); err != nil {
				return fmt.Errorf("release due account bans: %w", err)
			}
			siteIDs := make([]string, len(rows))
			for i, r := range rows {
				siteIDs[i] = r.SiteID
			}
			if tn, err = o.resolveTenancy(ctx, q, siteIDs, nil); err != nil {
				return err
			}
			changes = changes[:0]
			for _, r := range rows {
				changes = append(changes, expiredChange(tn, now, r.SiteID, SubjectAccount, r.ID, r.Hkey, r.FromState, StateActive, OpUnban, ReasonBanExpired))
			}
			_, err = insertStateEvents(ctx, tx, changes)
			return err
		})
		if err != nil {
			return err
		}
		bySite := map[string][]string{}
		for _, r := range rows {
			bySite[r.SiteID] = append(bySite[r.SiteID], r.ID)
			if s := tn.siteSnapshot[r.SiteID]; s != nil {
				if err := o.pushAccount(ctx, s, accountRow{ID: r.ID, Hkey: r.Hkey, SiteID: r.SiteID}, StateActive, 0, now); err != nil {
					o.retry.add(SubjectAccount, r.SiteID, []string{r.ID}, now)
				}
			}
		}
		for siteID, ids := range bySite {
			if err := o.syncAccounts(ctx, siteID, ids); err != nil {
				o.retry.add(SubjectAccount, siteID, ids, now)
			}
		}
		o.notify.publishAll(ctx, changes)
		if len(rows) < expiryBatch {
			return nil
		}
	}
	return nil
}

func (o *Operator) expireIdentityBans(ctx context.Context, now time.Time) error {
	for batch := 0; batch < maxExpiryBatches; batch++ {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		due, err := actiondb.New(o.pool).ActionDueIdentityBans(qctx, actiondb.ActionDueIdentityBansParams{Now: now, MaxRows: expiryBatch})
		cancel()
		if err != nil {
			return fmt.Errorf("query due identity bans: %w", err)
		}
		type target struct{ site, to string }
		groups := map[target][]string{}
		var order []target
		for _, r := range due {
			k := target{site: r.SiteID, to: o.banExpiryState(r.SiteID, r.Client)}
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], r.ID)
		}
		for _, k := range order {
			if err := o.releaseBans(ctx, now, k.site, k.to, groups[k]); err != nil {
				return err
			}
		}
		if len(due) < expiryBatch {
			return nil
		}
	}
	return nil
}

// banExpiryState returns the state identities of site+client enter when a
// temporary ban ends.
func (o *Operator) banExpiryState(siteID, client string) string {
	if s, _, ok := o.cat.Site(siteID); ok {
		if g, ok := s.Group(client, site.DefaultGroup); ok && g.Action != nil && g.Action.BanExpiryState == policy.BanExpiryActive {
			return StateActive
		}
	}
	return StatePending
}

func (o *Operator) releaseBans(ctx context.Context, now time.Time, siteID, to string, ids []string) error {
	var changed []changedIdentity
	var changes []StateChange
	var tn *tenancy
	err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
		rows, err := q.ActionReleaseDueBans(ctx, actiondb.ActionReleaseDueBansParams{ToState: to, Reason: ReasonBanExpired, Now: now, Ids: ids})
		if err != nil {
			return fmt.Errorf("release due identity bans: %w", err)
		}
		if tn, err = o.resolveTenancy(ctx, q, []string{siteID}, nil); err != nil {
			return err
		}
		changed, changes = changed[:0], changes[:0]
		for _, r := range rows {
			changed = append(changed, changedIdentity{ID: r.ID, Client: r.Client, TypeID: r.TypeID, Key: r.Hkey, From: r.FromState, To: to})
			changes = append(changes, expiredChange(tn, now, r.SiteID, SubjectIdentity, r.ID, r.Hkey, r.FromState, to, OpUnban, ReasonBanExpired))
		}
		_, err = insertStateEvents(ctx, tx, changes)
		return err
	})
	if err != nil {
		return err
	}
	o.pushExpired(ctx, tn, siteID, changed, to, now)
	o.notify.publishAll(ctx, changes)
	return nil
}

func (o *Operator) expireQuarantines(ctx context.Context, now time.Time) error {
	for batch := 0; batch < maxExpiryBatches; batch++ {
		var rows []actiondb.ActionReleaseDueQuarantinesRow
		var changes []StateChange
		var tn *tenancy
		err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
			var err error
			if rows, err = q.ActionReleaseDueQuarantines(ctx, actiondb.ActionReleaseDueQuarantinesParams{Reason: ReasonQuarantineEnded, Now: now, MaxRows: expiryBatch}); err != nil {
				return fmt.Errorf("release due quarantines: %w", err)
			}
			siteIDs := make([]string, len(rows))
			for i, r := range rows {
				siteIDs[i] = r.SiteID
			}
			if tn, err = o.resolveTenancy(ctx, q, siteIDs, nil); err != nil {
				return err
			}
			changes = changes[:0]
			for _, r := range rows {
				changes = append(changes, expiredChange(tn, now, r.SiteID, SubjectIdentity, r.ID, r.Hkey, r.FromState, StatePending, OpUnquarantine, ReasonQuarantineEnded))
			}
			_, err = insertStateEvents(ctx, tx, changes)
			return err
		})
		if err != nil {
			return err
		}
		bySite := map[string][]changedIdentity{}
		var order []string
		for _, r := range rows {
			if _, ok := bySite[r.SiteID]; !ok {
				order = append(order, r.SiteID)
			}
			bySite[r.SiteID] = append(bySite[r.SiteID], changedIdentity{ID: r.ID, Client: r.Client, TypeID: r.TypeID, Key: r.Hkey, From: r.FromState, To: StatePending})
		}
		for _, siteID := range order {
			o.pushExpired(ctx, tn, siteID, bySite[siteID], StatePending, now)
		}
		o.notify.publishAll(ctx, changes)
		if len(rows) < expiryBatch {
			return nil
		}
	}
	return nil
}

// pushExpired pushes identities of one site released at now to the hot state
// and records them for a retry when the push fails.
func (o *Operator) pushExpired(ctx context.Context, tn *tenancy, siteID string, changed []changedIdentity, to string, now time.Time) {
	if len(changed) == 0 {
		return
	}
	ids := make([]string, len(changed))
	for i, c := range changed {
		ids[i] = c.ID
	}
	var err error
	if s := tn.siteSnapshot[siteID]; s != nil {
		err = o.pushTransitions(ctx, s, changed, hotTransition{at: now, to: to})
	} else {
		err = o.syncIdentities(ctx, siteID, ids, SyncOptions{})
	}
	if err != nil {
		o.retry.add(SubjectIdentity, siteID, ids, now)
	}
}

func (o *Operator) expireProxies(ctx context.Context, now time.Time) error {
	for batch := 0; batch < maxExpiryBatches; batch++ {
		var rows []actiondb.ActionReleaseDueProxiesRow
		var changes []StateChange
		err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
			var err error
			if rows, err = q.ActionReleaseDueProxies(ctx, actiondb.ActionReleaseDueProxiesParams{Reason: ReasonBanExpired, Now: now, MaxRows: expiryBatch}); err != nil {
				return fmt.Errorf("release due proxies: %w", err)
			}
			nsIDs := make([]string, len(rows))
			for i, r := range rows {
				nsIDs[i] = r.NamespaceID
			}
			tn, err := o.resolveTenancy(ctx, q, nil, nsIDs)
			if err != nil {
				return err
			}
			changes = changes[:0]
			for _, r := range rows {
				action := OpUnban
				if r.FromState == StateQuarantined {
					action = OpUnquarantine
				}
				changes = append(changes, StateChange{
					At: now, TenantID: tn.tenantOfNS[r.NamespaceID], NamespaceID: r.NamespaceID,
					SubjectKind: SubjectProxy, SubjectID: r.ID, SubjectKey: r.Hkey, FromState: r.FromState, ToState: StateActive,
					Action: action, Scope: SubjectProxy, Actor: ActorSystem, Reason: ReasonBanExpired,
				})
			}
			_, err = insertStateEvents(ctx, tx, changes)
			return err
		})
		if err != nil {
			return err
		}
		byNS := map[string][]string{}
		for _, r := range rows {
			byNS[r.NamespaceID] = append(byNS[r.NamespaceID], r.ID)
		}
		for nsID, ids := range byNS {
			if err := o.syncProxies(ctx, nsID, ids); err != nil {
				o.retry.add(SubjectProxy, nsID, ids, now)
			}
		}
		o.notify.publishAll(ctx, changes)
		if len(rows) < expiryBatch {
			return nil
		}
	}
	return nil
}
