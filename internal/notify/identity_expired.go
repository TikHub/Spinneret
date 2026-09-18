package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/notify/notifydb"
)

// identity_expired alerts have two sources that produce identical alerts
// (same de-duplication key: identity ID and transition time in milliseconds):
//
//   - identity.state bus events, converted within milliseconds by the bus loop
//     of every instance. The bus queue is bounded, so a burst of expirations
//     (a bulk operation, a site-wide authentication failure) can overflow it.
//   - state_events, the source of truth, scanned by the alert evaluation job
//     on the leader (recoverExpiredIdentities). The scan resumes from a
//     watermark stored in system_settings and re-reads the last
//     expiredSettleWindow of transitions on every run, because the
//     StateWriter persists them asynchronously with their event time.
//     Transitions whose de-duplication marker still exists were alerted and
//     are skipped with one pipelined round trip per page.
//
// Delivery is at least once: a transition inside the settle window is visited
// again by the runs of the next expiredSettleWindow, which only alert it again
// when its marker (identityExpiredDedupTTL) expired in between, that is after
// a leader outage of about that long.
const (
	// expiredWatermarkKey is the system_settings key of the scan watermark.
	expiredWatermarkKey = "notify.identity_expired.watermark"
	// expiredSettleWindow bounds how long after its event time a transition
	// is expected to be persisted.
	expiredSettleWindow = 2 * time.Minute
	// expiredPageSize is the number of transitions read per query.
	expiredPageSize = 500
	// expiredRecoveryTimeBudget bounds the alerts one evaluation run emits
	// from state_events; the rest is emitted by the next runs.
	expiredRecoveryTimeBudget = 10 * time.Second
	// identityExpiredDedupTTL is the de-duplication window of identity_expired
	// alerts; it outlasts the settle window by far so that re-reading a
	// transition does not alert it again.
	identityExpiredDedupTTL = time.Hour
	// expiredWatermarkTimeout bounds storing the watermark, which also
	// happens when the run's context ended so that its progress is kept.
	expiredWatermarkTimeout = 5 * time.Second
)

// expiredWatermark is the stored scan position: every transition with an
// event time before SettledAt was visited after it was persisted.
type expiredWatermark struct {
	SettledAt time.Time `json:"settled_at"`
}

// expiredTransition is an enforced transition of an identity to "expired".
type expiredTransition struct {
	identityID string
	siteID     string
	from       string
	reason     string
	at         time.Time
}

// identityRef is the stored site, client and type of an identity.
type identityRef struct {
	siteID string
	client string
	typeID string
}

// expiredAlert builds the identity_expired alert of a transition. ref is nil
// when the identity no longer exists. It reports false when the transition
// cannot be attributed to a site.
func expiredAlert(ns *catalog.Namespace, tr expiredTransition, ref *identityRef) (Alert, bool) {
	siteID := tr.siteID
	var siteName, typeName, client string
	if ref != nil {
		siteID = firstNonEmpty(siteID, ref.siteID)
		client = ref.client
	}
	if siteID == "" {
		return Alert{}, false
	}
	if site, ok := ns.SitesByID[siteID]; ok {
		siteName = site.Name
		if ref != nil && ref.typeID != "" {
			if t, ok := site.IdentityTypesByID[ref.typeID]; ok {
				typeName = t.Name
			}
		}
	}
	details := map[string]any{
		"identity_id": tr.identityID, "site": siteName, "site_id": siteID, "type": typeName,
		"client": client, "reason": tr.reason, "from": tr.from,
	}
	message := "Identity " + tr.identityID
	if typeName != "" {
		message += " of type " + typeName
	}
	message += " on site " + firstNonEmpty(siteName, siteID) + " expired"
	if tr.reason != "" {
		message += ": " + tr.reason
	}
	message += "."
	return Alert{
		Kind: KindIdentityExpired, Severity: SeverityInfo, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: siteID,
		Title:    "Identity expired on " + firstNonEmpty(siteName, siteID),
		Message:  message,
		Details:  details,
		DedupKey: "identity_expired:" + tr.identityID + ":" + strconv.FormatInt(tr.at.UnixMilli(), 10),
		DedupTTL: identityExpiredDedupTTL,
	}, true
}

// expiredScan is the progress of one recovery run.
type expiredScan struct {
	// afterAt/afterID is the keyset position of the last visited transition.
	afterAt time.Time
	afterID string
	visited bool
	// caughtUp is set when every persisted transition was visited.
	caughtUp bool
	// recovered counts the alerts emitted by the run.
	recovered int
}

// recoverExpiredIdentities emits the identity_expired alerts of the
// transitions recorded in state_events that were not alerted yet (see the
// comment above expiredWatermarkKey). A run stops at its time budget and the
// next run resumes; a transient emit failure stops the run and is returned,
// while a transition whose alert can never be stored (permanentEmitError) is
// logged and skipped.
func (s *Service) recoverExpiredIdentities(ctx context.Context) error {
	runStart := s.now().UTC()
	settled := runStart.Add(-expiredSettleWindow)
	mark, found, err := s.loadExpiredWatermark(ctx)
	if err != nil {
		return err
	}
	if !found {
		// First run: older transitions are history, not pending alerts.
		mark.SettledAt = settled
	}
	within := s.expiredRecoveryBudget
	if within == nil {
		deadline := time.Now().Add(expiredRecoveryTimeBudget)
		within = func() bool { return time.Now().Before(deadline) }
	}
	scan := &expiredScan{afterAt: mark.SettledAt}
	runErr := s.scanExpiredTransitions(ctx, scan, within)

	next := mark.SettledAt
	switch {
	case scan.caughtUp:
		next = maxTime(next, settled)
	case scan.visited:
		next = maxTime(next, minTime(scan.afterAt, settled))
	}
	if !found || next.After(mark.SettledAt) {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), expiredWatermarkTimeout)
		err := s.storeExpiredWatermark(sctx, expiredWatermark{SettledAt: next})
		cancel()
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if scan.recovered > 0 {
		s.logger.Info("identity_expired alerts recovered from state events", slog.Int("alerts", scan.recovered))
	}
	return runErr
}

// scanExpiredTransitions visits transitions page by page from the scan
// position until every persisted one was visited, the budget is exhausted,
// ctx ends or an emit fails transiently.
func (s *Service) scanExpiredTransitions(ctx context.Context, scan *expiredScan, within func() bool) error {
	for page := 0; ctx.Err() == nil; page++ {
		if page > 0 && !within() {
			return nil
		}
		rows, err := s.q.NotifyExpiredIdentityEvents(ctx, notifydb.NotifyExpiredIdentityEventsParams{
			AfterAt: scan.afterAt, AfterID: scan.afterID, LimitRows: expiredPageSize,
		})
		if err != nil {
			return fmt.Errorf("list expired identity transitions: %w", err)
		}
		done, err := s.recoverExpiredPage(ctx, scan, rows, within)
		if err != nil || !done {
			return err
		}
		if len(rows) < expiredPageSize {
			scan.caughtUp = true
			return nil
		}
	}
	return ctx.Err()
}

// recoverExpiredPage emits the missing alerts of one page. It reports false
// when the run must stop (budget exhausted) before the page was finished.
func (s *Service) recoverExpiredPage(ctx context.Context, scan *expiredScan, rows []notifydb.NotifyExpiredIdentityEventsRow,
	within func() bool,
) (bool, error) {
	if len(rows) == 0 {
		return true, nil
	}
	alerts, err := s.expiredPageAlerts(ctx, rows)
	if err != nil {
		return false, err
	}
	pending, err := s.alertsNotDeduplicated(ctx, alerts)
	if err != nil {
		return false, err
	}
	for i, row := range rows {
		if pending[i] {
			if !within() {
				return false, nil
			}
			id, err := s.emit(ctx, alerts[i])
			switch {
			case err == nil:
				if id != "" {
					scan.recovered++
				}
			case permanentEmitError(err):
				// Retrying cannot store this alert; stopping here would keep
				// every later run from reaching the transitions after it.
				s.logger.Error("identity_expired alert cannot be stored, skipping the transition",
					slog.String("state_event_id", row.ID), slog.String("identity_id", row.SubjectID), slog.Any("error", err))
			default:
				return false, fmt.Errorf("emit identity_expired alert of %s: %w", row.SubjectID, err)
			}
		}
		scan.afterAt, scan.afterID, scan.visited = row.CreatedAt, row.ID, true
	}
	return true, nil
}

// permanentEmitError reports whether an emit failure is caused by the alert
// itself (a validation error, or values PostgreSQL rejects: SQLSTATE classes
// 22 and 23), so that emitting the same alert again cannot succeed.
func permanentEmitError(err error) bool {
	if e, ok := apperr.As(err); ok && e.Code == connect.CodeInvalidArgument {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
}

// expiredPageAlerts builds the alert of every row; rows that cannot alert (an
// unknown namespace or site) get a zero Alert.
func (s *Service) expiredPageAlerts(ctx context.Context, rows []notifydb.NotifyExpiredIdentityEventsRow) ([]Alert, error) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.SubjectID
	}
	refRows, err := s.q.NotifyIdentityRefs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load expired identities: %w", err)
	}
	refs := make(map[string]*identityRef, len(refRows))
	for _, r := range refRows {
		refs[r.ID] = &identityRef{siteID: r.SiteID, client: r.Client, typeID: r.TypeID}
	}
	alerts := make([]Alert, len(rows))
	for i, row := range rows {
		ns, ok := s.cat.Namespace(row.NamespaceID)
		if !ok {
			continue
		}
		tr := expiredTransition{
			identityID: row.SubjectID, siteID: row.SiteID, from: row.FromState, reason: row.Reason, at: row.CreatedAt,
		}
		if a, ok := expiredAlert(ns, tr, refs[row.SubjectID]); ok {
			alerts[i] = a
		}
	}
	return alerts, nil
}

// alertsNotDeduplicated reports, per alert, whether it has a kind and no
// de-duplication marker yet, with one pipelined round trip.
func (s *Service) alertsNotDeduplicated(ctx context.Context, alerts []Alert) ([]bool, error) {
	pending := make([]bool, len(alerts))
	cmds := make(rueidis.Commands, 0, len(alerts))
	index := make([]int, 0, len(alerts))
	for i, a := range alerts {
		if a.Kind == "" {
			continue
		}
		cmds = append(cmds, s.rdb.B().Exists().Key(s.dedupKeyOf(a)).Build())
		index = append(index, i)
	}
	if len(cmds) == 0 {
		return pending, nil
	}
	rctx, cancel := context.WithTimeout(ctx, dedupTimeout)
	defer cancel()
	for j, res := range s.rdb.DoMulti(rctx, cmds...) {
		n, err := res.AsInt64()
		if err != nil {
			return nil, fmt.Errorf("check alert dedup keys: %w", err)
		}
		pending[index[j]] = n == 0
	}
	return pending, nil
}

// loadExpiredWatermark reads the stored scan watermark.
func (s *Service) loadExpiredWatermark(ctx context.Context) (expiredWatermark, bool, error) {
	raw, err := s.q.NotifySettingGet(ctx, expiredWatermarkKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return expiredWatermark{}, false, nil
	}
	if err != nil {
		return expiredWatermark{}, false, fmt.Errorf("load identity_expired watermark: %w", err)
	}
	var mark expiredWatermark
	decodeErr := json.Unmarshal(raw, &mark)
	if decodeErr == nil && !mark.SettledAt.IsZero() {
		return mark, true, nil
	}
	// A corrupted document restarts the scan from the settle window instead
	// of stopping the recovery.
	s.logger.Warn("ignoring malformed identity_expired watermark", slog.String("value", string(raw)), slog.Any("error", decodeErr))
	return expiredWatermark{}, false, nil
}

// storeExpiredWatermark persists the scan watermark.
func (s *Service) storeExpiredWatermark(ctx context.Context, mark expiredWatermark) error {
	raw, err := json.Marshal(mark)
	if err != nil {
		return fmt.Errorf("encode identity_expired watermark: %w", err)
	}
	if err := s.q.NotifySettingPut(ctx, notifydb.NotifySettingPutParams{Key: expiredWatermarkKey, Value: raw}); err != nil {
		return fmt.Errorf("store identity_expired watermark: %w", err)
	}
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
