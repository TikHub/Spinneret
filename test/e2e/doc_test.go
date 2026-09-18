//go:build e2e

// Package e2e holds the end-to-end scenarios that run against a deployed Spinneret stack
// (deploy/compose/docker-compose.yml + docker-compose.e2e.yml): two server replicas behind the Caddy load
// balancer, PostgreSQL, Valkey, ClickHouse and the mock target site/proxy (test/mocktarget).
//
// Run them inside the compose network with `make e2e`. Every run creates its own namespace, site, identities,
// proxies, policies, token and notification channel through the admin APIs and never depends on seed data;
// the namespace is deleted at the end of a successful run unless SPINNERET_E2E_KEEP is set.
//
// Environment (defaults target a stack published on localhost):
//
//	SPINNERET_E2E_URL             load balancer URL                      (http://localhost:8080)
//	SPINNERET_E2E_ADMIN_USER      platform administrator                 (admin)
//	SPINNERET_E2E_ADMIN_PASSWORD  its password                           (required, the suite is skipped without it)
//	SPINNERET_E2E_TENANT          tenant name                            (default)
//	SPINNERET_E2E_MOCK_URL        mock target site + admin API           (http://localhost:19090)
//	SPINNERET_E2E_TARGET_URL      mock target as seen by the mock proxy  (SPINNERET_E2E_MOCK_URL)
//	SPINNERET_E2E_PROXY_HOST      mock proxy host:port stored in Spinneret (mocktarget:9091)
//	SPINNERET_E2E_CLIENT_PROXY_HOST  host:port the test dials instead of the stored proxy host (empty = same)
//	SPINNERET_E2E_PROXY_PASSWORD  mock proxy password                    (secret)
//	SPINNERET_E2E_REDIS           Valkey address                         (localhost:6379)
//	SPINNERET_E2E_REDIS_PREFIX    Spinneret Redis key prefix             (sp)
//	SPINNERET_E2E_REPORT_SHARDS   SPINNERET_REPORT_SHARDS of the servers (16)
//	SPINNERET_E2E_REPLICAS        server replicas behind the LB          (2)
//	SPINNERET_E2E_CALLBACK        webhook sink URL reachable by servers  (http://e2e:18099)
//	SPINNERET_E2E_SINK_ADDR       webhook sink listen address            (:18099)
//	SPINNERET_E2E_CRAWL_DURATION  duration of the crawler simulation     (60s)
//	SPINNERET_E2E_KEEP            keep the run's namespace when set
package e2e
