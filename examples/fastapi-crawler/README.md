# FastAPI example crawler

[中文文档](README.zh-CN.md)

A small crawler node built with [FastAPI](https://fastapi.tiangolo.com/) and the
[Spinneret Python SDK](../../sdk/python). Every HTTP call to the crawler:

1. **leases** an identity (cookies + User-Agent) and a proxy from Spinneret for the URI it is about to fetch
   (`AsyncClient.lease(site, client="web", uri=...)`),
2. **requests** the target site with `httpx.AsyncClient(**lease.httpx_kwargs())` — credential and proxy are
   merged into the client arguments,
3. **reports** the facts it observed (`lease.report_response(response, markers=[...])`): status, latency, size
   and page markers such as `captcha_page` or `login_redirect`. The report releases the lease when the
   `async with` block ends.

Spinneret turns the reports into outcomes (signal policy), cooldowns, bans and expiry (action policy) and
circuit breaking — the crawler never decides that an identity is burnt. The crawler also watches the config
item `crawler/example.json` with a `ConfigWatcher` (long polling).

In the demo the target is the Spinneret **mock target** (`test/mocktarget`), which serves `/site/...` pages and
an authenticating HTTP proxy, and announces page features in `X-Mock-Marker` / `X-Mock-Business-Code` response
headers (a real crawler would parse the page instead).

## Endpoints

| Method and path | Description |
| --- | --- |
| `GET /crawl/search?q=<text>` | Crawls `/site/search?q=<text>` (endpoint group `search`) |
| `GET /crawl/item/{id}` | Crawls `/site/item/{id}` (endpoint group `detail`) |
| `GET /config` | Current version and content of `crawler/example.json` from the config watcher |
| `GET /healthz` | Liveness |

A crawl answers `200` with `{"ok", "status", "identity_id", "proxy_id", "endpoint_group", "markers",
"business_code", "data"}`. Lease failures are mapped to HTTP answers: `503` for `circuit_open` / `site_paused`,
`429` for `no_identity_available` / `no_proxy_available` (both with `Retry-After` from the server hint), `502`
for other Spinneret errors and for requests that never got a response (reported with their `error_kind`).

## Quick start (Docker Compose)

Prerequisites: the stack in `deploy/compose` is initialized (`scripts/compose-init.sh`, `docker compose ... up`,
`init-admin`), plus `python3` and `curl` on the host.

```bash
scripts/example-quickstart.sh            # or: make example
```

The script is idempotent. It

1. starts the stack without recreating running containers and the mock target (profile `example`),
2. signs in as the administrator through the API and creates, in namespace `default`: site `example` with the
   endpoint groups `search` (prefix `/site/search`) and `detail` (template `/site/item/{id}`), the identity type
   `example_web_cookie`, 20 identities, 2 mock proxies, the published policies `example-rotation`
   (`bind_identity` proxies, 1 s reuse interval) and `example-signal`, the config item `crawler/example.json`,
3. creates a node token (`lease:acquire:example`, `report:write:example`, `config:read:crawler`) and writes it to
   `deploy/compose/.env` as `EXAMPLE_TOKEN` (an existing valid token is reused),
4. builds and starts the `example-crawler` service on `http://localhost:18000` and calls `/crawl/search`,
   `/crawl/item/42` and `/config`.

`scripts/example-quickstart.sh --reset` deletes the example site, its token, proxies, policies and config first.

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

Make the target misbehave and watch Spinneret react in the console (identities, breakers, request explorer):

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_URL` | — (compose: `http://lb:8080`) | Spinneret base URL (read by the SDK) |
| `SPINNERET_TOKEN` | — (compose: `EXAMPLE_TOKEN`) | Node token (read by the SDK) |
| `SPINNERET_NODE` | host name (compose: `example-crawler`) | Node name sent with every call |
| `SPINNERET_CACHE_DIR` | `/tmp/spinneret-cache` in the image | Config snapshot directory |
| `MOCK_TARGET_URL` | `http://mocktarget:9090` | Base URL of the crawled site |
| `EXAMPLE_SITE` / `EXAMPLE_CLIENT` | `example` / `web` | Site and client the leases are taken for |
| `EXAMPLE_CONFIG_GROUP` / `EXAMPLE_CONFIG_KEY` | `crawler` / `example.json` | Watched config item |
| `EXAMPLE_LEASE_WAIT_MS` | `2000` | Acquire wait for a free identity (0..5000) |
| `EXAMPLE_REQUEST_TIMEOUT_S` | `10` | Target request timeout (1..120) |
| `EXAMPLE_PORT` (compose) | `18000` | Host port of the crawler |

## Development

```bash
cd examples/fastapi-crawler
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt -e ../../sdk/python
.venv/bin/python -m pytest -q                                   # unit tests (respx, no network)
EXAMPLE_URL=http://localhost:18000 .venv/bin/python -m pytest -q tests/test_integration.py
SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_... MOCK_TARGET_URL=http://localhost:19090 \
  .venv/bin/uvicorn app.main:app --port 8000
```

Running outside Docker, the mock proxies stored in Spinneret (`mocktarget:9091`) are not resolvable from the
host; use the compose service or add `127.0.0.1 mocktarget` to `/etc/hosts` and publish port 9091.

## Troubleshooting

- `429 no_proxy_available`: the mock proxies were marked `dead` by the proxy health checks, which fetch
  `SPINNERET_PROXY_CHECK_URL` (the server's built-in check URL when unset) through every proxy. On a host
  without internet access set `SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz` in
  `deploy/compose/.env` and restart the `spinneret` service.
- `503 site_paused`: the site switch is off (`BreakerAdminService/SetSitePaused` or the console).
- `503 circuit_open`: the endpoint group breaker is open; it closes again once the target recovers.

## Production notes

- One `spinneret.AsyncClient` per process and event loop; create it in the lifespan (after forking workers) and
  `await client.aclose()` on shutdown so that queued reports (lease releases) are delivered.
- The example opens one `httpx.AsyncClient` per lease because httpx binds proxies to the client; a busy node
  can keep a small pool of clients keyed by proxy URL.
- Report facts only. Keep outcome classification in the signal policy, where it can be changed and tested
  (`PolicyAdminService/DebugReport`) without redeploying nodes.
- Never log lease credentials or proxy URLs; the SDK models hide them from `repr`.
