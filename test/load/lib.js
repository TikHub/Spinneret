// Shared helpers for Spinneret k6 load tests.
import http from 'k6/http';

export const BASE_URL = __ENV.SPINNERET_URL || 'http://localhost:8080';
export const TOKEN = __ENV.SPINNERET_TOKEN || '';
export const SITE = __ENV.SITE || 'loadtest';
export const CLIENT = __ENV.CLIENT || 'web';
export const GROUPS = parseInt(__ENV.GROUPS || '50', 10);

const HEADERS = {
  'Content-Type': 'application/json',
  'Connect-Protocol-Version': '1',
  Authorization: `Bearer ${TOKEN}`,
  'X-Spinneret-Node': `k6-${__VU || 0}`,
};

// url returns the request path of a Connect unary RPC.
export function url(service, method) {
  return `${BASE_URL}/spinneret.v1.${service}/${method}`;
}

// params returns the k6 request parameters (headers, tags, timeout) of an RPC.
export function params(method, extraTags = {}, timeout = '70s') {
  return { headers: HEADERS, tags: Object.assign({ name: method }, extraTags), timeout };
}

// call invokes a Connect unary RPC with JSON and tags the request by method name.
export function call(service, method, body, extraTags = {}) {
  return http.post(url(service, method), JSON.stringify(body), params(method, extraTags));
}

// groupURI returns the request path of endpoint group i as created by `spnr seed`.
export function groupURI(i) {
  return `/api/g${i}/items`;
}

export function uuid4() {
  // RFC 4122 v4 from Math.random is sufficient for report de-duplication keys in tests.
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

export function nowISO(offsetMs = 0) {
  return new Date(Date.now() + offsetMs).toISOString();
}

// connectError returns the Connect error code of a failed response, or the HTTP status.
export function connectError(res) {
  if (res.status === 200) return '';
  try {
    const body = JSON.parse(res.body);
    if (body && body.code) return body.code;
  } catch (e) {
    // Not a Connect error body (load balancer error page, timeout, ...).
  }
  return `http_${res.status}`;
}

// connectReason returns the Spinneret-Reason header of a failed response, or ''.
// The header is what distinguishes the failure modes that share one Connect code:
// unavailable is sent for an open breaker, a paused site and an admission-control
// shed alike, and resource_exhausted for an empty identity pool and for a missing
// proxy.
export function connectReason(res) {
  const h = res.headers || {};
  return h['Spinneret-Reason'] || h['spinneret-reason'] || '';
}

// summaryMetrics flattens the k6 summary into a small JSON object: counters and
// rates as numbers, trends as their statistics. test/load/run.sh stores it next
// to the server-side metric delta of the same run.
export function summaryMetrics(data) {
  const out = {};
  for (const [name, m] of Object.entries(data.metrics || {})) {
    if (m.type === 'trend') {
      out[name] = m.values;
    } else if (m.type === 'counter') {
      out[name] = { count: m.values.count, rate: m.values.rate };
    } else if (m.type === 'rate') {
      out[name] = { rate: m.values.rate, passes: m.values.passes, fails: m.values.fails };
    } else if (m.type === 'gauge') {
      out[name] = { value: m.values.value, min: m.values.min, max: m.values.max };
    }
  }
  const thresholds = {};
  for (const [name, m] of Object.entries(data.metrics || {})) {
    for (const [expr, t] of Object.entries(m.thresholds || {})) {
      thresholds[`${name} ${expr}`] = t.ok === undefined ? !t.fails : t.ok;
    }
  }
  return { metrics: out, thresholds };
}

// textSummary renders the metrics as aligned lines. k6's own renderer lives in a
// jslib that has to be downloaded at runtime; these tests must run offline.
function textSummary(summary) {
  const lines = [];
  const names = Object.keys(summary.metrics).sort();
  for (const name of names) {
    const v = summary.metrics[name];
    let text;
    if (v.count !== undefined && v.rate !== undefined && v.avg === undefined) {
      text = `count=${v.count} rate=${v.rate.toFixed(2)}/s`;
    } else if (v.avg !== undefined) {
      const f = (x) => (x === undefined ? '-' : x.toFixed(2));
      text = `avg=${f(v.avg)} med=${f(v.med)} p90=${f(v['p(90)'])} p95=${f(v['p(95)'])} p99=${f(v['p(99)'])} max=${f(v.max)}`;
    } else if (v.rate !== undefined) {
      text = `rate=${(v.rate * 100).toFixed(2)}% passes=${v.passes} fails=${v.fails}`;
    } else {
      text = `value=${v.value} min=${v.min} max=${v.max}`;
    }
    lines.push(`  ${name.padEnd(42)} ${text}`);
  }
  for (const [name, ok] of Object.entries(summary.thresholds)) {
    lines.push(`  ${ok ? 'PASS' : 'FAIL'} threshold ${name}`);
  }
  return lines.join('\n');
}

// handleSummary is re-exported by every scenario so that the run prints a readable
// summary and a machine-readable block between the markers below (parsed by
// test/load/run.sh).
export function handleSummary(data) {
  const summary = summaryMetrics(data);
  return {
    stdout: `\n${textSummary(summary)}\n\n---K6SUMMARY---\n${JSON.stringify(summary)}\n---K6SUMMARYEND---\n`,
  };
}
