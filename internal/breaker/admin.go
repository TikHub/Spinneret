package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakerdb"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// Manual operation limits.
const (
	// MaxManualOpenDuration caps timed manual opens; longer outages should be
	// opened indefinitely.
	MaxManualOpenDuration = 365 * 24 * time.Hour
	// MaxReasonLength bounds reasons stored with manual operations (bytes).
	MaxReasonLength = 1024
)

// Audit actions.
const (
	AuditBreakerOpen  = "breaker.open"
	AuditBreakerClose = "breaker.close"
	AuditSitePause    = "site.pause"
	AuditSiteResume   = "site.resume"
)

// SiteSwitch is the state of a site switch.
type SiteSwitch struct {
	SiteID   string
	Site     string
	Paused   bool
	Reason   string
	PausedAt *time.Time
	PausedBy string
	// Changed reports whether the operation changed the paused flag.
	Changed bool
}

// siteSwitchUpdate is the result of the site switch transaction.
type siteSwitchUpdate struct {
	SiteSwitch
	// contentChanged reports whether the runtime content changed (flag or
	// reason of a paused site).
	contentChanged bool
}

// ParseOpenDuration parses a manual open duration: "" and "permanent" mean
// indefinite (0); other values must be positive and at most
// MaxManualOpenDuration.
func ParseOpenDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := durationx.Parse(s)
	if err != nil {
		return 0, apperr.InvalidArgument("", "duration: %v", err)
	}
	if d.IsPermanent() {
		return 0, nil
	}
	if d.Std() <= 0 {
		return 0, apperr.InvalidArgument("", "duration must be positive")
	}
	if d.Std() > MaxManualOpenDuration {
		return 0, apperr.InvalidArgument("", "duration must not exceed %s (use an indefinite open)", durationx.Duration(MaxManualOpenDuration))
	}
	return d.Std(), nil
}

func validateReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxReasonLength {
		return "", apperr.InvalidArgument("", "reason must be at most %d bytes", MaxReasonLength)
	}
	return reason, nil
}

// Open manually opens the breaker of an endpoint group (breaker:operate on its
// site) for duration ("" or "permanent" = until closed manually). It records a
// breaker event with trigger manual, publishes the transition and writes an
// audit entry. Recent cooldowns are not reverted for manual opens.
func (s *Service) Open(ctx context.Context, p *authz.Principal, groupID, duration, reason string) (*Status, error) {
	d, err := ParseOpenDuration(duration)
	if err != nil {
		return nil, err
	}
	return s.operate(ctx, p, groupID, "open", d, reason)
}

// Close manually closes the breaker of an endpoint group (breaker:operate on
// its site). Closing a closed breaker changes nothing but is still audited.
func (s *Service) Close(ctx context.Context, p *authz.Principal, groupID, reason string) (*Status, error) {
	return s.operate(ctx, p, groupID, "close", 0, reason)
}

func (s *Service) operate(ctx context.Context, p *authz.Principal, groupID, op string, d time.Duration, reason string) (*Status, error) {
	reason, err := validateReason(reason)
	if err != nil {
		return nil, err
	}
	ns, site, g, err := s.resolveGroup(p, groupID)
	if err != nil {
		return nil, err
	}
	action := AuditBreakerOpen
	if op == "close" {
		action = AuditBreakerClose
	}
	if err := p.Require(authz.PermBreakerOperate, siteResource(ns, site)); err != nil {
		s.recordDenied(ctx, p, ns, action, "endpoint_group", g.ID, g.Name, authz.PermBreakerOperate)
		return nil, err
	}
	details := map[string]any{"site": site.Name, "client": g.Client, "reason": reason}
	if op == "open" {
		details["duration"] = durationLabel(d)
	}

	// Event metrics describe the window the operator acted on; a close
	// restarts the window, so it is read before the change.
	target := []candidate{{ns: ns, site: site, group: g}}
	var window Window
	if before, err := s.readStatuses(ctx, target); err == nil {
		window = statusWindow(before[0])
	}

	now := s.now()
	call := setExec(s.keys, site.Key, g.Key, now, op, d, reason)
	sctx, cancel := s.ioContext(ctx)
	res, err := parseSetResult(setScript.Exec(sctx, s.rdb, call.Keys, call.Args))
	cancel()
	if err != nil {
		details["error"] = "breaker update failed"
		s.recordAudit(ctx, p, ns, action, "endpoint_group", g.ID, g.Name, audit.ResultError, details)
		return nil, apperr.Internal(fmt.Errorf("%s breaker of group %s: %w", op, g.ID, err))
	}
	details["from"], details["to"], details["changed"] = res.From, res.To, res.Changed

	statuses, readErr := s.readStatuses(ctx, target)
	if res.LazyHalfOpen {
		// Record the half-open transition acquire.lua performed before this
		// operation replaced it.
		lazy := res.Hash
		lazy.State, lazy.Reason, lazy.Manual, lazy.OpenUntilMs = StateHalfOpen, res.LazyReason, res.LazyManual, 0
		lazy.Version = max(res.Hash.Version-1, 0)
		if err := s.recordTransition(ctx, transitionRecord{
			ns: ns, site: site, group: g, at: millisTime(res.LazyAtMs),
			from: StateOpen, to: StateHalfOpen, trigger: TriggerAuto, actor: actorSystem,
			hash: lazy, window: window,
		}); err != nil {
			s.logger.Error("record lazy half-open transition failed",
				slog.String("endpoint_group_id", g.ID), slog.Any("error", err))
		}
	}
	if res.Changed {
		rec := transitionRecord{
			ns: ns, site: site, group: g, at: now,
			from: res.From, to: res.To, trigger: TriggerManual, actor: p.Actor(),
			hash: res.Hash, window: window,
		}
		if err := s.recordTransition(ctx, rec); err != nil {
			s.logger.Error("record manual breaker transition failed",
				slog.String("endpoint_group_id", g.ID), slog.Any("error", err))
		}
	}
	s.recordAudit(ctx, p, ns, action, "endpoint_group", g.ID, g.Name, audit.ResultOK, details)
	if readErr != nil {
		// The operation succeeded; answer with the state the script returned
		// instead of failing the call.
		s.logger.Warn("read breaker status after manual operation failed",
			slog.String("endpoint_group_id", g.ID), slog.Any("error", readErr))
		current := window
		if op == "close" && res.Changed {
			current = Window{} // a close restarts the window
		}
		st := buildStatus(target[0], res.Hash, current, now)
		return &st, nil
	}
	return &statuses[0], nil
}

func statusWindow(st Status) Window {
	return Window{Total: st.Window.Total, Success: st.Window.Success, Risk: st.Window.Risk, CaptchaIdentities: st.Window.CaptchaIdentities}
}

func durationLabel(d time.Duration) string {
	if d <= 0 {
		return "indefinite"
	}
	return durationx.Duration(d).String()
}

// SetSitePaused turns the site switch on (paused) or off (breaker:operate on
// the site). It updates sites.paused*, mirrors the flag into the hot-state meta
// hash, invalidates the catalog and, when the switch changed, records a
// breaker_events row (trigger site_switch, from/to "running"/"paused"), bumps
// the "site_switches" and "breakers" runtime versions (breakers content
// carries the paused flag) and publishes the events. The operation is audited.
func (s *Service) SetSitePaused(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, siteName string, paused bool, reason string) (SiteSwitch, error) {
	if p == nil {
		return SiteSwitch{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	reason, err := validateReason(reason)
	if err != nil {
		return SiteSwitch{}, err
	}
	site, ok := ns.Sites[siteName]
	if !ok || siteName == "" {
		return SiteSwitch{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", siteName)
	}
	action := AuditSiteResume
	if paused {
		action = AuditSitePause
	}
	if err := p.Require(authz.PermBreakerOperate, siteResource(ns, site)); err != nil {
		s.recordDenied(ctx, p, ns, action, "site", site.ID, site.Name, authz.PermBreakerOperate)
		return SiteSwitch{}, err
	}
	details := map[string]any{"paused": paused, "reason": reason}

	now := s.now().UTC()
	update, err := s.updateSitePaused(ctx, p, ns, site, paused, reason, now)
	if err != nil {
		s.recordAudit(ctx, p, ns, action, "site", site.ID, site.Name, audit.ResultError, details)
		return SiteSwitch{}, err
	}
	result := update.SiteSwitch
	details["changed"] = result.Changed

	var errs []error
	if err := s.mirrorSitePaused(ctx, site, paused); err != nil {
		errs = append(errs, err)
	}
	if err := s.cat.Invalidate(ctx, ns.ID); err != nil {
		errs = append(errs, fmt.Errorf("invalidate catalog of namespace %s: %w", ns.ID, err))
	}
	if update.contentChanged {
		if _, err := s.bumpRuntime(ctx, ns, KindSiteSwitches); err != nil {
			errs = append(errs, err)
		}
		if _, err := s.bumpRuntime(ctx, ns, KindBreakers); err != nil {
			errs = append(errs, err)
		}
	}
	if result.Changed {
		if err := s.publishNamespace(ctx, ns, site.ID, now, siteSwitchData(ns, site, paused, reason, p.Actor())); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		// PostgreSQL is the source of truth; peers converge through the
		// periodic catalog reload and hot-state sync.
		s.logger.Error("propagate site switch failed", slog.String("site_id", site.ID), slog.Any("error", err))
	}
	s.recordAudit(ctx, p, ns, action, "site", site.ID, site.Name, audit.ResultOK, details)
	return result, nil
}

// updateSitePaused updates the site row and records the site_switch breaker
// event in one transaction.
func (s *Service) updateSitePaused(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site *catalog.Site, paused bool, reason string, now time.Time) (siteSwitchUpdate, error) {
	out := siteSwitchUpdate{SiteSwitch: SiteSwitch{SiteID: site.ID, Site: site.Name}}
	ctx, cancel := s.ioContext(ctx)
	defer cancel()
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		q := breakerdb.New(tx)
		prev, err := q.BreakerLockSite(ctx, breakerdb.BreakerLockSiteParams{ID: site.ID, NamespaceID: ns.ID})
		if err != nil {
			return pgstore.MapError(err, fmt.Sprintf("site %q", site.Name))
		}
		arg := breakerdb.BreakerUpdateSitePausedParams{ID: site.ID, NamespaceID: ns.ID, Paused: paused}
		if paused {
			arg.PausedReason = reason
			arg.PausedBy = p.Actor()
			arg.PausedAt = &now
			if prev.Paused && prev.PausedAt != nil {
				arg.PausedAt = prev.PausedAt
				arg.PausedBy = prev.PausedBy
			}
		}
		row, err := q.BreakerUpdateSitePaused(ctx, arg)
		if err != nil {
			return pgstore.MapError(err, fmt.Sprintf("site %q", site.Name))
		}
		out.Paused, out.Reason, out.PausedAt, out.PausedBy = row.Paused, row.PausedReason, row.PausedAt, row.PausedBy
		out.Changed = prev.Paused != paused
		out.contentChanged = out.Changed || (paused && prev.PausedReason != reason)
		if !out.Changed {
			return nil
		}
		from, to := SiteRunning, SitePaused
		if !paused {
			from, to = SitePaused, SiteRunning
		}
		metrics, err := json.Marshal(map[string]any{})
		if err != nil {
			return fmt.Errorf("marshal site switch metrics: %w", err)
		}
		return q.BreakerInsertEvent(ctx, breakerdb.BreakerInsertEventParams{
			ID:          idgen.New(idgen.BreakerEvent),
			CreatedAt:   now,
			TenantID:    ns.TenantID,
			NamespaceID: ns.ID,
			SiteID:      site.ID,
			FromState:   from,
			ToState:     to,
			Trigger:     TriggerSiteSwitch,
			Reason:      reason,
			Metrics:     metrics,
			Actor:       p.Actor(),
		})
	})
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return siteSwitchUpdate{}, err
		}
		return siteSwitchUpdate{}, apperr.Internal(fmt.Errorf("set paused of site %s: %w", site.ID, err))
	}
	return out, nil
}

// mirrorSitePaused writes the paused flag into the hot-state meta hash.
func (s *Service) mirrorSitePaused(ctx context.Context, site *catalog.Site, paused bool) error {
	flag := "0"
	if paused {
		flag = "1"
	}
	ctx, cancel := s.ioContext(ctx)
	defer cancel()
	if err := sitePausedScript.Exec(ctx, s.rdb, []string{s.keys.SiteMeta(site.Key)}, []string{flag}).Error(); err != nil {
		return fmt.Errorf("mirror paused flag of site %s: %w", site.ID, err)
	}
	return nil
}

func siteSwitchData(ns *catalog.Namespace, site *catalog.Site, paused bool, reason, actor string) TransitionData {
	from, to := SiteRunning, SitePaused
	if !paused {
		from, to = SitePaused, SiteRunning
	}
	return TransitionData{
		Namespace: ns.Name,
		Site:      site.Name,
		SiteID:    site.ID,
		From:      from,
		To:        to,
		Trigger:   TriggerSiteSwitch,
		Reason:    reason,
		Manual:    true,
		Actor:     actor,
	}
}
