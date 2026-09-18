# mocktarget

`mocktarget` is a small, dependency-free Go program used by the Spinneret end-to-end and load tests. It runs
two listeners in one process that share state:

| Listener | Default address | Purpose |
| --- | --- | --- |
| Target site + admin API | `:9090` (`SPINNERET_MOCK_ADDR`) | Fake crawl target whose answers are scripted at runtime |
| HTTP forward proxy | `:9091` (`SPINNERET_MOCK_PROXY_ADDR`) | Authenticating proxy that tags traffic with a proxy id |

Tests script deterministic situations (429, captcha pages, login redirects, 5xx, empty lists, business error
codes, latency, flaky upstreams, dead or misbehaving proxies) and then assert on the counters under
`/_admin/stats`.

> The admin API has no authentication. Only run mocktarget on test networks.

## Running

```bash
go run ./test/mocktarget                      # serve until SIGINT/SIGTERM
go run ./test/mocktarget healthcheck          # exit 0 when the local site answers /healthz

docker build -f deploy/docker/mocktarget.Dockerfile -t spinneret-mocktarget:local .
docker run --rm -p 9090:9090 -p 9091:9091 spinneret-mocktarget:local
```

The image is distroless (`nonroot`); its `HEALTHCHECK` runs `mocktarget healthcheck`.

### Configuration

Unset or empty variables use the default.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_MOCK_ADDR` | `:9090` | Target site and admin API listen address |
| `SPINNERET_MOCK_PROXY_ADDR` | `:9091` | Forward proxy listen address |
| `SPINNERET_MOCK_PROXY_PASSWORD` | `secret` | Password every proxy id must present (taken verbatim) |
| `SPINNERET_MOCK_LOG_LEVEL` | `info` | `debug` logs every request (too verbose for load tests) |
| `SPINNERET_MOCK_LOG_FORMAT` | `json` | `json` or `text` (slog) |
| `SPINNERET_MOCK_SEED` | `0` (random) | Seed of the probability draws; the effective seed is logged at startup |
| `SPINNERET_MOCK_STATS_MAX_KEYS` | `100000` | Distinct stats keys kept per listener before folding into `_overflow` |
| `SPINNERET_MOCK_SHUTDOWN_TIMEOUT` | `10s` | Grace period for in-flight requests on shutdown (open CONNECT tunnels are closed immediately; a second signal exits at once) |
| `SPINNERET_MOCK_DIAL_TIMEOUT` | `10s` | Proxy upstream dial timeout |
| `SPINNERET_MOCK_TUNNEL_IDLE_TIMEOUT` | `5m` | A CONNECT tunnel with no bytes in either direction for this long is closed |

## Target site

`GET` or `POST` any path under `/site/` returns, when no rule applies:

```json
{"ok":true,"path":"/site/search","identity":"dev-1","proxy":"p1","items":[{"id":"/site/search#1","title":"item 1"}, ...]}
```

Each request is attributed to:

- **identity**: cookie `sessionid`, else query parameter `device_id`, else `anonymous`;
- **proxy**: the proxy id of the CONNECT tunnel the connection came through, else header `X-Mock-Proxy-Id`
  (injected by the mock proxy on forwarded requests), else `direct`.

Every `/site/` response carries `X-Mock-Mode` (the behavior actually served), `X-Mock-Identity`, `X-Mock-Proxy`
and, when a rule matched, `X-Mock-Rule` (its prefix). Bodies are never cached (`Cache-Control: no-store`).
Rules always match the full identity and proxy values; the labels echoed in headers, bodies and statistics are
truncated to 256 bytes.

Other endpoints: `GET /healthz` → `{"ok":true}`; `GET /login` → HTML page containing `login-page`
(the destination of `login_redirect`).

### Modes

| Mode | Default status | Response |
| --- | --- | --- |
| `ok` | 200 | JSON item list (`item_count` items, default 3) |
| `rate_limit` | 429 | `{"ok":false,"error":"rate_limited"}`; `status` may be any 4xx/5xx |
| `captcha` | 200 | HTML containing `captcha-page`; header `X-Mock-Marker: captcha_page` |
| `login_redirect` | 302 | `Location: /login`; header `X-Mock-Marker: login_redirect`; `status` ∈ 301 302 303 307 308 |
| `server_error` | 503 | `{"ok":false,"error":"server_error"}`; `status` may be any 5xx |
| `empty` | 200 | `{"ok":true,"path":…,"items":[]}`; header `X-Mock-Marker: empty_list` |
| `business_error` | 200 | `{"ok":false,"code":<business_code>,"message":"business error"}`; header `X-Mock-Business-Code` |
| `slow` | 200 | Sleeps `latency_ms` (default 1000) then answers like `ok` |
| `flaky` | 429 | With `probability` (default 0.5) answers like `rate_limit`, otherwise like `ok` |

`ok`, `slow`, `empty`, `captcha` and `business_error` accept a 2xx `status` other than 204/205.
`business_code` is rendered as a JSON number when it is a canonical integer (`10001`) and as a string otherwise
(`"E_BLOCKED"`, `"007"`).

### Rules: `PUT /_admin/rules`

The body replaces all rules atomically. Unknown fields and invalid values are rejected with
`400 {"ok":false,"error":"rule 0: …"}` and the previous rules stay in force.

```json
[
  {"prefix": "/site/search", "mode": "rate_limit", "status": 429},
  {"prefix": "/site/search", "mode": "captcha", "identities": ["sess-a", "dev-7"]},
  {"prefix": "/site/feed", "mode": "flaky", "probability": 0.3, "proxies": ["p1", "direct"]},
  {"prefix": "/site/detail", "mode": "business_error", "business_code": "10001"},
  {"prefix": "/site/", "mode": "slow", "latency_ms": 250, "item_count": 20}
]
```

| Field | Default | Meaning |
| --- | --- | --- |
| `prefix` | required | Plain string prefix of the request path; must start with `/` |
| `mode` | required | One of the modes above |
| `status` | per mode | Status override, validated per mode |
| `business_code` | `10001` | Only for `business_error`; string or number |
| `probability` | `1.0` (`flaky`: `0.5`) | Chance in [0,1] that the rule triggers; otherwise the request is served as `ok` |
| `latency_ms` | `0` (`slow`: `1000`) | Sleep before a triggered rule answers (any mode), max 120000 |
| `item_count` | `3` | Items in `ok`-style bodies, 0..1000 |
| `identities` | all | Only apply to these identities |
| `proxies` | all | Only apply to these proxy ids (`direct` = no proxy) |

Matching: rules are tried by longest `prefix` first; among equal prefixes, rules with both filters come before
rules with one filter, which come before unfiltered rules; remaining ties keep submission order. The first rule
whose prefix, identity filter and proxy filter all match is applied, so a filtered rule that does not match
falls back to the next applicable (possibly shorter) rule. No applicable rule means `ok`.

Probability draws use `math/rand/v2` PCG generators seeded per request from `SPINNERET_MOCK_SEED` and a request
counter: no shared lock, and reproducible for a fixed seed and request order. Use `probability` 0 or 1 when a test
needs exact outcomes.

- `GET /_admin/rules` returns the normalized rules (defaults filled in) in submission order.
- `DELETE /_admin/rules` removes all rules (everything answers `ok`) and returns `[]`.
- `PUT` answers with the normalized rules.

## Forward proxy

Point an HTTP client at `http://<proxyId>:<password>@mocktarget:9091`.

- **Authentication**: `Proxy-Authorization: Basic base64(<proxyId>:<password>)`. Missing/malformed credentials or a
  wrong password → `407` with `Proxy-Authenticate: Basic realm="mocktarget"`. The username is the proxy id.
- **Absolute-form requests** (`GET http://host/path HTTP/1.1`, what clients send for `http://` URLs): forwarded
  with hop-by-hop headers and `Proxy-Authorization` removed and `X-Mock-Proxy-Id: <proxyId>` set (a client-supplied
  value is overwritten). Bodies are relayed as-is (no compression negotiated). Upstream failures → `502`; a
  response whose body breaks off mid-stream is aborted by closing the client connection.
- **CONNECT tunnels** (`CONNECT host:port`): `200 Connection Established`, then raw bytes are piped both ways
  (half-close aware; closed once idle in both directions for `SPINNERET_MOCK_TUNNEL_IDLE_TIMEOUT`). Unreachable
  target → `502`; target without port → `400`. Plain HTTP sent through a tunnel to the mock target is attributed
  to the proxy id because both listeners share a registry of tunnel connections (no header needed).
- Origin-form requests other than `GET /healthz` → `400`.

### Proxy behavior: `PUT /_admin/proxies`

```json
[
  {"proxy_id": "p1", "mode": "refuse"},
  {"proxy_id": "p2", "mode": "auth_fail"},
  {"proxy_id": "p3", "mode": "slow", "latency_ms": 2000},
  {"proxy_id": "*", "mode": "ok"}
]
```

| Mode | Behavior |
| --- | --- |
| `ok` | Normal proxying (default for ids without a rule) |
| `refuse` | Closes the client connection without any response (TCP reset where possible) |
| `auth_fail` | `407` even with correct credentials |
| `slow` | Sleeps `latency_ms` (default 1000) before acting |

`latency_ms` (0..120000) applies before any mode. `proxy_id` `"*"` applies to ids without their own rule; ids must be
unique. `refuse` and `auth_fail` apply before the password check. `GET /_admin/proxies` returns the rules,
`DELETE /_admin/proxies` resets every proxy to `ok`.

## Statistics

`GET /_admin/stats` returns counters of both listeners since start or the last reset:

```json
{
  "total": 6,
  "by_path": {"/site/search": 5, "/site/feed": 1},
  "by_mode": {"ok": 3, "rate_limit": 3},
  "by_identity": {"dev-1": 4, "dev-2": 2},
  "by_proxy": {"p1": 4, "direct": 2},
  "by_status": {"200": 3, "429": 3},
  "entries": [
    {"path": "/site/search", "rule": "/site/search", "mode": "rate_limit", "identity": "dev-1", "proxy": "p1", "status": 429, "count": 3}
  ],
  "overflowed": 0,
  "proxy_listener": {
    "total": 5,
    "by_proxy": {"p1": 4, "dead": 1},
    "by_result": {"forwarded": 4, "refused": 1},
    "entries": [{"proxy_id": "p1", "result": "forwarded", "count": 4}],
    "overflowed": 0
  }
}
```

- `mode` is the behavior actually served (a triggered `flaky` rule counts as `rate_limit`, an untriggered rule as
  `ok`); `rule` is the matched prefix (`""` when none). `status` `499` means the client went away during the
  scripted latency.
- Proxy results: `forwarded`, `tunneled`, `auth_missing` (id `_unknown`), `auth_invalid`, `auth_fail`, `refused`,
  `bad_request`, `upstream_error` (dial or response header failure), `canceled` (client left before the upstream
  answered), `aborted` (the response body copy failed mid-stream).
- Query parameters: `path_prefix`, `identity`, `proxy`, `mode` filter the site counters (`proxy` also filters the
  proxy listener counters); `entries=false` omits the per-key entries.
- Once a listener holds `SPINNERET_MOCK_STATS_MAX_KEYS` distinct keys, new keys are folded into `_overflow` labels
  (mode, status and result stay exact) and `overflowed` counts those increments.

`POST /_admin/reset` clears the statistics of both listeners (rules are kept) and returns `{"ok":true}`.

## Example

```bash
curl -X PUT localhost:9090/_admin/rules \
  -d '[{"prefix":"/site/search","mode":"rate_limit","identities":["dev-1"]}]'
curl -x http://p1:secret@localhost:9091 'http://localhost:9090/site/search?device_id=dev-1'   # 429
curl -x http://p1:secret@localhost:9091 'http://localhost:9090/site/search?device_id=dev-2'   # 200
curl 'localhost:9090/_admin/stats?identity=dev-1&entries=false'
```
