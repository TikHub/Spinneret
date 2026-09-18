// Concurrent WatchConfig long polls. Design target (§18.4): >= 10,000 concurrent
// watchers per instance.
//
//   WATCHERS=10000 DURATION=2m k6 run watch_config.js
//
// One k6 VU costs a whole JS runtime, so 10,000 VUs do not fit on a laptop-sized
// machine. Each VU therefore holds POLLS_PER_VU long polls in parallel with
// http.batch (options.batch/batchPerHost are raised accordingly), which is what the
// server sees: WATCHERS blocked WatchConfig calls. spinneret_config_watchers on each
// replica is the authoritative number and is sampled during the run by
// test/load/run.sh.
//
// A watcher must hold the *current* version, otherwise the server answers at once and
// the test degenerates into a request flood: setup() reads the published version and
// every VU keeps it up to date from the changes it receives.
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import http from 'k6/http';
import { call, connectError, url, params } from './lib.js';

export { handleSummary } from './lib.js';

const WATCHERS = parseInt(__ENV.WATCHERS || '1000', 10);
const POLLS_PER_VU = parseInt(__ENV.POLLS_PER_VU || '10', 10);
const VUS = Math.max(1, Math.ceil(WATCHERS / POLLS_PER_VU));
const DURATION = __ENV.DURATION || '1m';
const TIMEOUT_MS = parseInt(__ENV.WATCH_TIMEOUT_MS || '30000', 10);
const GROUP = __ENV.CONFIG_GROUP || 'crawler';
const KEY = __ENV.CONFIG_KEY || 'loadtest.json';
// RAMP_S spreads the first poll of each VU over this many seconds. Without it every
// watcher opens its connection in the same millisecond; at 10,000 watchers that
// connection storm overruns the load balancer's dial timeout long before the server
// is busy (the single upstream is then ejected by its passive health check). Real
// crawler nodes start over minutes, so the ramp is the realistic shape.
const RAMP_S = parseFloat(__ENV.RAMP_S || '15');

const polls = new Counter('watch_polls');
const idle = new Counter('watch_idle_returns');
const changes = new Counter('watch_changes');
const failures = new Counter('watch_failures');
const awareness = new Trend('watch_awareness_ms', true);

export const options = {
  setupTimeout: '2m',
  discardResponseBodies: false,
  scenarios: {
    watchers: { executor: 'constant-vus', vus: VUS, duration: DURATION, gracefulStop: '40s' },
  },
  thresholds: { watch_failures: ['count<1'] },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  batch: POLLS_PER_VU,
  batchPerHost: POLLS_PER_VU,
};

export function setup() {
  const res = call('ConfigService', 'BatchGetConfig', { items: [{ group: GROUP, key: KEY }] });
  if (res.status !== 200) {
    throw new Error(`cannot read ${GROUP}/${KEY}: ${res.status} ${connectError(res)} ${String(res.body).slice(0, 200)}`);
  }
  const items = res.json('items') || [];
  if (items.length === 0) {
    throw new Error(`config item ${GROUP}/${KEY} is not published; run \`spnr seed\` first`);
  }
  const version = items[0].version;
  console.log(`watching ${GROUP}/${KEY} at version ${version} with ${VUS} VUs x ${POLLS_PER_VU} polls = ${VUS * POLLS_PER_VU} watchers`);
  return { version };
}

// held is per VU: k6 gives every VU its own module instance.
let held = -1;
let ramped = false;

export default function (data) {
  if (held < 0) {
    held = data.version;
  }
  if (!ramped) {
    ramped = true;
    if (RAMP_S > 0) {
      sleep((((__VU - 1) % VUS) / VUS) * RAMP_S);
    }
  }
  const body = JSON.stringify({ items: [{ group: GROUP, key: KEY, version: held }], timeout_ms: TIMEOUT_MS });
  const reqs = [];
  for (let i = 0; i < POLLS_PER_VU; i++) {
    reqs.push({
      method: 'POST',
      url: url('ConfigService', 'WatchConfig'),
      body,
      // The request timeout must exceed the long-poll timeout.
      params: params('WatchConfig', {}, `${TIMEOUT_MS + 20000}ms`),
    });
  }
  const received = http.batch(reqs);
  const now = Date.now();
  for (const res of received) {
    polls.add(1);
    if (!check(res, { 'watch ok': (r) => r.status === 200 })) {
      failures.add(1);
      console.error(`watch failed: ${res.status} ${connectError(res)} ${String(res.body).slice(0, 160)}`);
      continue;
    }
    const items = res.json('items') || [];
    if (items.length === 0) {
      idle.add(1);
      continue;
    }
    changes.add(items.length);
    if (items[0].version > held) {
      held = items[0].version;
    }
    // test/load/config_awareness.py publishes content carrying published_at_ms; when
    // the watched item has it, every woken watcher reports its own wake-up latency.
    try {
      const content = JSON.parse(items[0].content);
      if (content && content.published_at_ms) {
        awareness.add(now - content.published_at_ms);
      }
    } catch (e) {
      // Not JSON, or no publish timestamp: nothing to measure.
    }
  }
}
