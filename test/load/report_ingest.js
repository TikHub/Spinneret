// Report ingest throughput: batches of reports against a pool of long-lived leases.
// Target from the design doc (§18.4): >= 20,000 reports/s accepted per instance, and
// report → state update p99 < 200 ms (spinneret_report_lag_seconds).
//
//   BATCH=100 REPORT_RATE=200 DURATION=1m k6 run report_ingest.js   (200 batches/s x 100 = 20k reports/s)
//
// The leases are acquired once in setup() and are not released between reports, so a
// lease outlives its TTL (60 s in the seeded rotation policy) during a long run. Such
// reports stay *accepted* — the ingest path keeps the lease hash readable for
// SPINNERET_LATE_REPORT_WINDOW (10 min) after the lease ended — but the worker treats
// them as late and skips the state update, which does not affect ingest throughput.
// Keep DURATION at or below a few minutes so the whole run stays inside that window.
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import http from 'k6/http';
import { call, connectError, url, params, SITE, CLIENT, GROUPS, groupURI, uuid4, nowISO } from './lib.js';

export { handleSummary } from './lib.js';

const BATCH = parseInt(__ENV.BATCH || '100', 10);
const RATE = parseInt(__ENV.REPORT_RATE || '100', 10);
const DURATION = __ENV.DURATION || '1m';
const LEASES = parseInt(__ENV.LEASES || '200', 10);
const PRE_VUS = parseInt(__ENV.PRE_VUS || String(Math.max(20, Math.ceil(RATE / 4))), 10);
const MAX_VUS = parseInt(__ENV.MAX_VUS || String(Math.max(100, RATE * 2)), 10);

const accepted = new Counter('reports_accepted');
const duplicated = new Counter('reports_duplicated');
const rejected = new Counter('reports_rejected');
const failed = new Counter('report_calls_failed');
const batchLatency = new Trend('report_batch_latency', true);

export const options = {
  setupTimeout: '5m',
  discardResponseBodies: false,
  scenarios: {
    ingest: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PRE_VUS,
      maxVUs: MAX_VUS,
      gracefulStop: '30s',
    },
  },
  thresholds: {
    'report_batch_latency': ['p(99)<1000'],
    'reports_rejected': ['count<1'],
    'report_calls_failed': ['count<1'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  // setup() acquires the lease pool with parallel batches.
  batch: 64,
  batchPerHost: 64,
};

export function setup() {
  const leases = [];
  const chunk = 64;
  for (let start = 0; start < LEASES; start += chunk) {
    const reqs = [];
    for (let i = start; i < Math.min(start + chunk, LEASES); i++) {
      const uri = groupURI(i % GROUPS);
      reqs.push({
        method: 'POST',
        url: url('LeaseService', 'Acquire'),
        body: JSON.stringify({ site: SITE, client: CLIENT, uri, wait_ms: 2000 }),
        params: params('Acquire'),
      });
    }
    const responses = http.batch(reqs);
    responses.forEach((res, idx) => {
      if (res.status === 200) {
        leases.push({ id: res.json('lease.lease_id'), uri: groupURI((start + idx) % GROUPS) });
      }
    });
  }
  if (leases.length === 0) {
    throw new Error('could not acquire any lease for the ingest test');
  }
  console.log(`ingest setup: ${leases.length}/${LEASES} leases acquired`);
  return { leases };
}

export default function (data) {
  const reports = [];
  for (let i = 0; i < BATCH; i++) {
    const l = data.leases[Math.floor(Math.random() * data.leases.length)];
    reports.push({
      report_id: uuid4(),
      lease_id: l.id,
      uri: l.uri,
      method: 'GET',
      http_status: 200,
      latency_ms: 100,
      response_bytes: 1024,
      started_at: nowISO(-100),
      finished_at: nowISO(),
    });
  }
  const res = call('ReportService', 'Report', { reports });
  batchLatency.add(res.timings.duration);
  if (!check(res, { 'status 200': (r) => r.status === 200 })) {
    failed.add(1);
    console.error(`report batch failed: ${res.status} ${connectError(res)} ${String(res.body).slice(0, 200)}`);
    return;
  }
  const acc = res.json('accepted') || 0;
  const dup = res.json('duplicated') || 0;
  const rej = res.json('rejected') || [];
  accepted.add(acc);
  duplicated.add(dup);
  rejected.add(rej.length);
  if (rej.length > 0) {
    console.error(`rejected ${rej.length}: ${JSON.stringify(rej[0])}`);
  }
}

export function teardown(data) {
  for (const l of data.leases) {
    call('LeaseService', 'Release', { lease_id: l.id });
  }
}
