# End-to-end scenarios

`go test -tags e2e ./test/e2e/...` drives a **deployed** Spinneret stack through the public APIs: two server
replicas behind the Caddy load balancer, PostgreSQL, Valkey, ClickHouse and the mock target site/proxy
(`test/mocktarget`). Nothing is stubbed: leases are taken through the load balancer, requests really go through
the mock HTTP proxy to the mock site, reports travel through the Redis streams into the workers, and the
assertions read the admin APIs, the Redis hot state and ClickHouse.

```bash
make e2e          # build images, start the stack with the e2e overlay, run the suite in the compose network
```

which is

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.e2e.yml --profile test build
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.e2e.yml --profile test \
  up -d --wait spinneret lb mocktarget
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.e2e.yml --profile test \
  run --rm --use-aliases e2e
```

The overlay shortens the proxy health check interval to 5 s (against the mock target) and adds the `e2e`
service: `golang:1.27-alpine` with the repository mounted and module/build caches in named volumes.
`--use-aliases` gives the one-off container the DNS name `e2e`, so the servers can deliver webhook
notifications to the test's sink (`SPINNERET_E2E_CALLBACK=http://e2e:18099`).

Every run creates its own namespace (`e2e-<run id>`), site, identity types, identities, proxies, policies,
token and notification channel through the admin APIs — no seed data is used, runs never collide, and a
successful run deletes everything it created (`SPINNERET_E2E_KEEP=1` keeps it; a failed run always keeps it).
All environment variables are documented in `doc_test.go`.

## Scenarios (`TestStack`, in this order)

| Subtest | What it proves |
| --- | --- |
| `i_scale_out` | Both replicas register as live workers, split the 16 report shards, and the load balancer reaches every replica |
| `h_config_secret_runtime` | A config item referencing a vault secret is delivered resolved (`has_secret_refs`), a node reads the secret, `WatchConfig` on `_runtime/site_switches` wakes within milliseconds of `SetSitePaused`, paused sites refuse leases with `site_paused` |
| `b_crawler_simulation` | 8 goroutines × 60 s of acquire → request through the leased proxy → report(release): no identity is held twice, the reuse interval is respected per identity and endpoint group, `bind_identity` keeps one proxy per identity (verified against the mock target statistics), pending identities activate, every report reaches ClickHouse |
| `c_rate_limited_cooldown` | A 429 for one identity cools down exactly that identity × endpoint group (hot state + state event with the rule name); other identities and its other endpoint group stay usable |
| `d_captcha_ban_unban` | Three captcha pages ban the identity for 1 h (`captcha-ban` rule), the scheduler stops leasing it, a manual unban makes it leasable again |
| `e_login_redirect_expire_webhook` | A login redirect expires the identity and the `identity_expired` webhook arrives with a valid HMAC signature |
| `g_proxy_health_rebind` | A proxy that answers 407 is marked `dead` by the health checks, identities bound to it are rebound on the next acquire, and it revives when it works again |
| `f_breaker_open_half_open_close` | Rate limiting the whole endpoint group opens its breaker (`ListBreakers`, SSE `breaker.transition`, webhook), `Acquire` fails with `unavailable`/`circuit_open`, the neighbouring group stays closed, and half-open probes close it again |

Scenarios share the mock target, whose scripted rules are global, so they run sequentially and reset the rules
they set.

## Related drills

- `scripts/e2e-failover.sh` (`make e2e-failover`): stops one replica under load and measures the error rate,
  shard takeover and report backlog.
- `scripts/example-quickstart.sh` (`make example`): the FastAPI example node against the same stack.
- `test/load` (`make load`): k6 throughput scenarios.
