// Package testutil provides shared integration-test fixtures for Spinneret:
// isolated PostgreSQL databases cloned from a migrated template, Redis key
// prefixes and ClickHouse databases.
//
// Connection strings come from SPINNERET_TEST_DATABASE_URL,
// SPINNERET_TEST_REDIS_URL and SPINNERET_TEST_CLICKHOUSE_URL (exported by the
// test compose stack and the Makefile). When a variable is unset the fixture
// starts a throwaway container with testcontainers-go once per test binary;
// containers are reaped by the testcontainers Ryuk sidecar when the process
// exits. Every fixture calls t.Skip in -short mode and registers its own
// cleanup with t.Cleanup, so tests never need a TestMain.
package testutil
