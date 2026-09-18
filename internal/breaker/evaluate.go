package breaker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/redis/rueidis"
	"golang.org/x/sync/errgroup"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// EvaluateJobName is the name of the evaluation sweep job.
const EvaluateJobName = "breaker_evaluate"

// sitesPerPipeline bounds the candidate reads sent in one pipeline.
const sitesPerPipeline = 100

// EvaluateJob returns the evaluation sweep (every instance, EvalInterval).
// Each group is guarded by the per-site lock "lock:brk:<eg>", so instances
// never evaluate the same group concurrently.
func (s *Service) EvaluateJob() jobs.Job {
	return jobs.Job{
		Name:     EvaluateJobName,
		Interval: s.cfg.EvalInterval,
		Mode:     jobs.EachInstance,
		Timeout:  s.cfg.EvalInterval + s.cfg.LockTTL,
		Run:      s.Sweep,
	}
}

// candidate is an endpoint group selected for evaluation.
type candidate struct {
	ns    *catalog.Namespace
	site  *catalog.Site
	group *catalog.EndpointGroup
}

// Sweep evaluates every candidate group of every namespace once: groups with
// activity within ActiveWindow ("aeg") and groups whose breaker is not closed
// ("brko"). Failures of single groups do not stop the sweep; they are returned
// joined (bounded).
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now()
	var sites []candidate
	for _, ns := range s.cat.Namespaces("") {
		for _, site := range ns.SitesByID {
			sites = append(sites, candidate{ns: ns, site: site})
		}
	}
	collector := &errorCollector{}
	var groups []candidate
	for start := 0; start < len(sites); start += sitesPerPipeline {
		end := min(start+sitesPerPipeline, len(sites))
		found, err := s.candidates(ctx, sites[start:end], now)
		collector.add(err)
		groups = append(groups, found...)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.cfg.Concurrency)
	for _, c := range groups {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if _, err := s.evaluateGroup(gctx, c.ns, c.site, c.group); err != nil {
				collector.add(err)
			}
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return collector.err()
}

// candidates reads "aeg" and "brko" of the given sites in one pipeline and
// returns the endpoint groups known to the catalog.
func (s *Service) candidates(ctx context.Context, sites []candidate, now time.Time) ([]candidate, error) {
	since := strconv.FormatInt(now.Add(-s.cfg.ActiveWindow).UnixMilli(), 10)
	cmds := make(rueidis.Commands, 0, 2*len(sites))
	for _, c := range sites {
		cmds = append(cmds,
			s.rdb.B().Zrangebyscore().Key(s.keys.ActiveGroups(c.site.Key)).Min(since).Max("+inf").Build(),
			s.rdb.B().Smembers().Key(s.keys.OpenBreakers(c.site.Key)).Build(),
		)
	}
	ictx, cancel := s.ioContext(ctx)
	defer cancel()
	results := s.rdb.DoMulti(ictx, cmds...)
	var out []candidate
	var errs []error
	for i, c := range sites {
		seen := make(map[int64]struct{})
		for _, res := range results[2*i : 2*i+2] {
			members, err := res.AsStrSlice()
			if err != nil {
				errs = append(errs, fmt.Errorf("read breaker candidates of site %s: %w", c.site.ID, err))
				continue
			}
			for _, m := range members {
				key, err := strconv.ParseInt(m, 10, 64)
				if err != nil {
					continue
				}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				g, ok := c.site.GroupsByKey[key]
				if !ok {
					continue
				}
				out = append(out, candidate{ns: c.ns, site: c.site, group: g})
			}
		}
	}
	return out, errors.Join(errs...)
}

// evaluateGroup runs breaker_eval.lua for one group under its evaluation lock
// and records a transition when one happened. It returns false without error
// when another evaluation holds the lock.
func (s *Service) evaluateGroup(ctx context.Context, ns *catalog.Namespace, site *catalog.Site, g *catalog.EndpointGroup) (bool, error) {
	lockKey := s.keys.SiteLock(site.Key, "brk:"+strconv.FormatInt(g.Key, 10))
	owner := s.nextLockOwner()
	lctx, cancel := s.ioContext(ctx)
	ok, err := redis.TryLock(lctx, s.rdb, lockKey, owner, s.cfg.LockTTL)
	cancel()
	if err != nil {
		return false, fmt.Errorf("lock breaker of group %s: %w", g.ID, err)
	}
	if !ok {
		return false, nil
	}
	defer func() {
		uctx, ucancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.IOTimeout)
		defer ucancel()
		if err := redis.Unlock(uctx, s.rdb, lockKey, owner); err != nil {
			s.logger.Warn("release breaker lock failed", slog.String("endpoint_group_id", g.ID), slog.Any("error", err))
		}
	}()

	now := s.now()
	params := paramsFor(g.Breaker)
	call := evalExec(s.keys, site.Key, g.Key, now, params, modeEval)
	ectx, ecancel := s.ioContext(ctx)
	res, err := parseEvalResult(evalScript.Exec(ectx, s.rdb, call.Keys, call.Args))
	ecancel()
	if err != nil {
		return true, fmt.Errorf("evaluate breaker of group %s: %w", g.ID, err)
	}
	s.observeState(site, g, res.Hash.State)
	if res.LazyHalfOpen {
		s.logTransition(ns, site, g, StateOpen, StateHalfOpen, TriggerAuto, res.Hash.Reason, 0, 0)
		return true, s.recordTransition(ctx, transitionRecord{
			ns: ns, site: site, group: g, at: millisTime(res.LazyAtMs),
			from: StateOpen, to: StateHalfOpen, trigger: TriggerAuto, actor: actorSystem,
			hash: res.Hash, window: res.Window,
		})
	}
	if !res.Transitioned() {
		return true, nil
	}

	rec := transitionRecord{
		ns: ns, site: site, group: g, at: now,
		from: res.From, to: res.To, trigger: res.Trigger, actor: actorSystem,
		hash: res.Hash, window: res.Window,
		probeSamples: res.ProbeSamples, probeSuccesses: res.ProbeSuccesses,
	}
	var errs []error
	if res.To == StateOpen && (res.From == StateClosed || res.From == StateHalfOpen) {
		rec.revertedEndpoint, rec.revertedSite, err = s.revert(ctx, site, g, now, params)
		if err != nil {
			errs = append(errs, err)
		}
	}
	s.logTransition(ns, site, g, res.From, res.To, res.Trigger, res.Hash.Reason, rec.revertedEndpoint, rec.revertedSite)
	if err := s.recordTransition(ctx, rec); err != nil {
		errs = append(errs, err)
	}
	return true, errors.Join(errs...)
}

// logTransition logs an automatic transition.
func (s *Service) logTransition(ns *catalog.Namespace, site *catalog.Site, g *catalog.EndpointGroup, from, to, trigger, reason string, revertedEndpoint, revertedSite int64) {
	s.logger.Info("breaker transition",
		slog.String("namespace_id", ns.ID), slog.String("site", site.Name), slog.String("client", g.Client),
		slog.String("endpoint_group", g.Name), slog.String("from", from), slog.String("to", to),
		slog.String("trigger", trigger), slog.String("reason", reason),
		slog.Int64("reverted_endpoint_cooldowns", revertedEndpoint), slog.Int64("reverted_site_cooldowns", revertedSite))
}

// revert runs revert.lua for a group that just opened, according to its
// policy's revert_recent_cooldowns mode.
func (s *Service) revert(ctx context.Context, site *catalog.Site, g *catalog.EndpointGroup, now time.Time, params evalParams) (endpoint, siteCount int64, err error) {
	if params.Revert == policy.RevertNone {
		return 0, 0, nil
	}
	var groupKeys []int64
	if params.Revert == policy.RevertAll {
		groupKeys = make([]int64, 0, len(site.GroupsByKey))
		for key := range site.GroupsByKey {
			groupKeys = append(groupKeys, key)
		}
		slices.Sort(groupKeys)
	}
	call := revertExec(s.keys, site.Key, g.Key, now, params.WindowMs, params.Revert, groupKeys)
	rctx, cancel := s.ioContext(ctx)
	defer cancel()
	endpoint, siteCount, err = parseRevertResult(revertScript.Exec(rctx, s.rdb, call.Keys, call.Args))
	if err != nil {
		return 0, 0, fmt.Errorf("revert recent cooldowns of group %s: %w", g.ID, err)
	}
	return endpoint, siteCount, nil
}
