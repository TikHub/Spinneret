// Acquire → (simulated request) → Report(release) at a constant arrival rate.
// Targets from the design doc (§18.4): Acquire p99 < 5 ms server-side, >= 5000 acquires/s
// per instance. The authoritative latency is the server-side histogram
// (spinneret_acquire_duration_seconds); the k6 trend below also contains the
// Docker network and the Caddy load balancer.
//
//   ACQUIRE_RATE=5000 DURATION=2m k6 run acquire_report.js
//
// Failed acquires are counted by Connect code *and* Spinneret-Reason, so an admission
// control shed (unavailable/overloaded) is distinguishable from an open breaker, a paused
// site and an exhausted pool. `test/load/run.sh ADMISSION=on|off` switches the server gate
// and this scenario's expectations together; see test/load/README.md.
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { call, connectError, connectReason, SITE, CLIENT, GROUPS, groupURI, uuid4, nowISO } from './lib.js';

export { handleSummary } from './lib.js';

const RATE = parseInt(__ENV.ACQUIRE_RATE || '2000', 10);
const DURATION = __ENV.DURATION || '1m';
// REPORT_MODE isolates the parts of the cycle when profiling where the cost goes:
//   release (default) Acquire + Report(release=true): the full cycle, including the
//                     worker ending the lease.
//   keep              Acquire + Report(release=false): the lease is left to expire, so
//                     the ingest path is measured without the lease-end write.
//   none              Acquire only; leases expire on their own. Keep such runs short —
//                     a lease is held for lease_ttl (60 s in the seeded policy).
const REPORT_MODE = __ENV.REPORT_MODE || 'release';
// PRE_VUS/MAX_VUS bound the VU pool. One iteration is two sequential RPCs of about
// 1 ms, so ~RATE/300 VUs are busy at any time; the defaults leave ample headroom
// without allocating VUs the machine cannot afford.
const PRE_VUS = parseInt(__ENV.PRE_VUS || String(Math.max(50, Math.ceil(RATE / 20))), 10);
const MAX_VUS = parseInt(__ENV.MAX_VUS || String(Math.max(200, Math.ceil(RATE / 4))), 10);
// ADMISSION records which arm of the acquire admission control A/B this run is:
//   on  (default) the server has a gate (SPINNERET_ACQUIRE_FLEET_INFLIGHT > 0), so a shed
//                 (unavailable/overloaded) is a healthy answer to overload and is counted,
//                 not failed.
//   off           the server has no gate, so a shed is impossible; one means the arm was
//                 misconfigured and the acquire_shed threshold below fails the run.
// test/load/run.sh sets both this and the server variable, so both arms are one build.
const ADMISSION = (__ENV.ADMISSION || 'on').toLowerCase();
// SHED_MAX is the number of sheds the run still passes with, for the runs whose pass
// criterion is "no shedding at this rate" with the gate on (SHED_MAX=0). Unset means
// sheds are counted but never fail the run.
const SHED_MAX = Number.isFinite(parseInt(__ENV.SHED_MAX, 10)) ? parseInt(__ENV.SHED_MAX, 10) : -1;

// Acquire outcomes are counted by Connect code *and* Spinneret-Reason: unavailable covers
// an admission-control shed, an open breaker and a paused site, and resource_exhausted
// covers an empty identity pool and a missing proxy. Bucketing them together hides exactly
// the behaviour the admission control runs are measuring.
const acquireOK = new Counter('acquire_ok');
const acquireExhausted = new Counter('acquire_exhausted');
const acquireNoProxy = new Counter('acquire_no_proxy');
const acquireShed = new Counter('acquire_shed');
const acquireBreakerOpen = new Counter('acquire_breaker_open');
const acquireSitePaused = new Counter('acquire_site_paused');
const acquireUnavailable = new Counter('acquire_unavailable');
const acquireFailed = new Counter('acquire_failed');
const reportAccepted = new Counter('report_accepted');
const reportFailed = new Counter('report_failed');
const acquireLatency = new Trend('acquire_latency', true);
const reportLatency = new Trend('report_latency', true);

// A shed is not counted as acquire_failed, so the acquire_failed threshold keeps its exact
// current meaning: a shed is a healthy response to overload, an error is not.
const thresholds = {
  // End-to-end latency includes the Docker network and the load balancer.
  acquire_latency: ['p(99)<50'],
  acquire_failed: ['count<1'],
  report_failed: ['count<1'],
};
if (ADMISSION === 'off') {
  thresholds.acquire_shed = ['count<1'];
} else if (SHED_MAX >= 0) {
  thresholds.acquire_shed = [`count<${SHED_MAX + 1}`];
}

export const options = {
  discardResponseBodies: false,
  noConnectionReuse: false,
  tags: { admission: ADMISSION },
  scenarios: {
    acquire_report: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PRE_VUS,
      maxVUs: MAX_VUS,
      gracefulStop: '30s',
    },
  },
  thresholds,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const g = Math.floor(Math.random() * GROUPS);
  const started = nowISO();
  const res = call('LeaseService', 'Acquire', { site: SITE, client: CLIENT, uri: groupURI(g) });
  acquireLatency.add(res.timings.duration);
  if (res.status === 200) {
    acquireOK.add(1);
  } else {
    const code = connectError(res);
    const reason = connectReason(res);
    if (code === 'resource_exhausted') {
      if (reason === 'no_proxy_available') {
        acquireNoProxy.add(1);
      } else {
        acquireExhausted.add(1);
      }
    } else if (code === 'unavailable') {
      if (reason === 'overloaded') {
        acquireShed.add(1);
      } else if (reason === 'circuit_open') {
        acquireBreakerOpen.add(1);
      } else if (reason === 'site_paused') {
        acquireSitePaused.add(1);
      } else {
        acquireUnavailable.add(1);
      }
    } else {
      acquireFailed.add(1);
      console.error(`acquire failed: ${res.status} ${code} ${reason} ${String(res.body).slice(0, 200)}`);
    }
    return;
  }
  if (REPORT_MODE === 'none') {
    return;
  }
  const lease = res.json('lease');
  const outcome = Math.random() < 0.97 ? 200 : 429;
  const rep = call('ReportService', 'Report', {
    reports: [
      {
        report_id: uuid4(),
        lease_id: lease.lease_id,
        uri: groupURI(g),
        method: 'GET',
        http_status: outcome,
        latency_ms: 120 + Math.floor(Math.random() * 200),
        response_bytes: 4096,
        started_at: started,
        finished_at: nowISO(),
        release: REPORT_MODE !== 'keep',
      },
    ],
  });
  reportLatency.add(rep.timings.duration);
  if (!check(rep, { 'report accepted': (r) => r.status === 200 && r.json('accepted') === 1 })) {
    reportFailed.add(1);
    if (rep.status !== 200) {
      console.error(`report failed: ${rep.status} ${connectError(rep)} ${String(rep.body).slice(0, 200)}`);
    }
    return;
  }
  reportAccepted.add(rep.json('accepted'));
}
