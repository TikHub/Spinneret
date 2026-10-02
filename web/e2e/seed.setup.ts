import { SEEDED_SITE } from './fixtures/env';
import { ensureSeededSite, sendSeedTraffic } from './fixtures/seed';
import { test as setup } from './fixtures/test';

/**
 * Prepares the shared site every run needs (see fixtures/seed.ts): it is
 * created on a fresh deployment, completed on one that lacks part of it, and
 * given fresh traffic every time. Nothing has to be seeded by hand.
 */
setup('seed the shared site with identities and fresh traffic', async ({ api }) => {
  // The import, the traffic and the wait for ClickHouse take seconds; the
  // margin is for a deployment that is still warming up.
  setup.setTimeout(180_000);
  await ensureSeededSite(api, SEEDED_SITE);
  await sendSeedTraffic(api, SEEDED_SITE);
});
