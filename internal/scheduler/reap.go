package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Lease reaper tuning (spec §6.8).
const (
	reapJobName     = "lease_reaper"
	reapInterval    = time.Second
	reapJobTimeout  = 5 * time.Second
	reapLockName    = "reap"
	reapLockTTL     = 900 * time.Millisecond
	reapSiteBudget  = 800 * time.Millisecond
	reapParallelism = 8
	// reapBatch leases per script call; kept below the 500 maximum so that
	// one call (which also rescores identities) stays short on Redis.
	reapBatch = 200
)

// ReapJob returns the lease reaper: every second, on every instance, each site
// whose per-site lock this instance wins has its overdue leases expired.
func (s *Service) ReapJob() jobs.Job {
	return jobs.Job{
		Name:     reapJobName,
		Interval: reapInterval,
		Mode:     jobs.EachInstance,
		Timeout:  reapJobTimeout,
		Run:      s.reapOnce,
	}
}

// reapSite is one site to reap.
type reapSite struct {
	ns   *catalog.Namespace
	site *catalog.Site
}

// reapOnce runs one reaper iteration over every site of the catalog.
func (s *Service) reapOnce(ctx context.Context) error {
	var sites []reapSite
	for _, ns := range s.cat.Namespaces("") {
		for _, st := range ns.SitesByID {
			sites = append(sites, reapSite{ns: ns, site: st})
		}
	}
	if len(sites) == 0 {
		return nil
	}
	work := make(chan reapSite)
	errs := make([]error, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := min(reapParallelism, len(sites))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rs := range work {
				if _, err := s.reapSite(ctx, rs.ns, rs.site); err != nil && ctx.Err() == nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("reap site %s: %w", rs.site.ID, err))
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for _, rs := range sites {
		select {
		case work <- rs:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	return errors.Join(errs...)
}

// reapSite expires the overdue leases of one site while holding its reaper
// lock. It returns the number of leases expired.
func (s *Service) reapSite(ctx context.Context, ns *catalog.Namespace, st *catalog.Site) (int, error) {
	lockKey := s.keys.SiteLock(st.Key, reapLockName)
	ok, err := redis.TryLock(ctx, s.rdb, lockKey, s.owner, reapLockTTL)
	if err != nil || !ok {
		return 0, err
	}
	started := time.Now()
	layout := s.layouts.layout(st)
	total := 0
	for {
		n, scanned, err := s.reapBatch(ctx, ns, st, layout)
		total += n
		if err != nil {
			return total, err
		}
		if scanned < reapBatch || time.Since(started) >= reapSiteBudget {
			return total, nil
		}
		select {
		case <-ctx.Done():
			// The job ended: stop early. The remaining backlog is picked up
			// by the next pass, so cancellation is not a failure.
			return total, nil
		default:
		}
	}
}

// reapBatch runs reap.lua once and records statistics of the expired leases.
func (s *Service) reapBatch(ctx context.Context, ns *catalog.Namespace, st *catalog.Site, layout []string) (expired, scanned int, err error) {
	now := s.clock()
	args := make([]string, 0, 3+len(layout))
	args = append(args, itoa(now.UnixMilli()), itoa(reapBatch), itoa(s.cfg.LateReportWindow.Milliseconds()))
	args = append(args, layout...)
	cctx, cancel := scriptContext(ctx, false)
	defer cancel()
	vals, err := replyStrings(reapScript.Exec(cctx, s.rdb, []string{s.keys.SiteMeta(st.Key)}, args))
	if err != nil {
		return 0, 0, fmt.Errorf("run reap script: %w", err)
	}
	if len(vals) < 1 || (len(vals)-1)%reapLeaseFields != 0 {
		return 0, 0, fmt.Errorf("reap: malformed reply (%d elements)", len(vals))
	}
	scanned = int(atoi64(vals[0]))
	for i := 1; i+reapLeaseFields <= len(vals); i += reapLeaseFields {
		f := vals[i : i+reapLeaseFields]
		kind := EndExpired
		if f[6] == "1" {
			kind = EndAbandoned
		}
		s.recordLeaseEnd(ns, st, kind, now, f[0], atoi64(f[3]), f[1], f[2], f[4], f[5], f[7] == "1")
		if s.metrics != nil {
			s.metrics.LeaseReaped.WithLabelValues(metricLabel(st.Name), kind).Inc()
		}
		expired++
	}
	if expired > 0 {
		s.logger.Debug("leases reaped", slog.String("site_id", st.ID), slog.Int("count", expired))
	}
	return expired, scanned, nil
}
