import { expect } from '@playwright/test';

import { type Api } from './api';

/**
 * The one site the suite shares instead of creating per spec: the overview,
 * the heatmap, the request explorer, the rule debugger and the README
 * screenshots read it. seed.setup.ts creates whatever it is missing and sends
 * it fresh traffic before every run, and nothing deletes it, so it carries
 * history across runs the way a real site does.
 */

/** Endpoint groups of the seeded site besides `_default`. */
export const SEEDED_GROUPS = ['search', 'detail'] as const;

/** Identity type of the seeded identities. */
export const SEEDED_TYPE = 'seed_token';

/**
 * Identities of the seeded site. Every rate-limited report cools one down for
 * a minute or more, so there have to be enough left for the screenshot spec's
 * own burst of traffic.
 */
export const SEEDED_IDENTITIES = 40;

/**
 * Acquire/report cycles per run. The request explorer opens on the last hour,
 * so every run sends its own instead of relying on what an earlier one left.
 */
export const SEEDED_REQUESTS = 30;

const TYPE_SPEC = `name: ${SEEDED_TYPE}
client: web
description: Synthetic identities of the console e2e suite (web/e2e/seed.setup.ts)
fields:
  token: { type: string, required: true, sensitive: true }
unique_by: [token]
activation: immediate
deliver:
  headers:
    Authorization: "Bearer {{ token }}"
`;

/** The same rows every run, so a re-import creates nothing and changes nothing. */
function identityRows(site: string): Record<string, unknown>[] {
  return Array.from({ length: SEEDED_IDENTITIES }, (_, i) => {
    const n = String(i + 1).padStart(2, '0');
    return { token: `${site}-seed-${n}`, _labels: { name: `seed-${n}` } };
  });
}

/** Creates what the seeded site is missing; whatever exists is left as it is. */
export async function ensureSeededSite(api: Api, site: string): Promise<void> {
  const existing = await api.getSite(site);
  if (!existing) {
    await api.createSite(site, {
      displayName: 'Smoke',
      description: 'Shared site of the console e2e suite (web/e2e/seed.setup.ts).',
    });
  } else if (!existing.clients.includes('web')) {
    throw new Error(
      `site ${site} exists without a "web" client; set SPINNERET_E2E_SEEDED_SITE to a site the suite may use`,
    );
  }

  const groups = await api.endpointGroupNames(site);
  for (const group of SEEDED_GROUPS) {
    if (!groups.includes(group)) await api.createEndpointGroup(site, group);
  }

  if (!(await api.hasIdentityType(site, SEEDED_TYPE))) await api.createIdentityType(site, TYPE_SPEC);
  await api.importIdentities(site, SEEDED_TYPE, identityRows(site));
}

/**
 * Sends one run's traffic to the seeded site through the node API and waits
 * until the request explorer can see it, so no spec races the asynchronous
 * report pipeline.
 */
export async function sendSeedTraffic(api: Api, site: string): Promise<void> {
  const tokenName = `e2e-seed-${site}`;
  // An interrupted run can leave the token usable, and names are unique among
  // usable tokens.
  await api.revokeTokenByName(tokenName);
  const token = await api.createNodeToken(tokenName);
  const since = new Date();
  try {
    await api.seedTraffic(token, site, SEEDED_REQUESTS, ['_default', ...SEEDED_GROUPS]);
  } finally {
    await api.revokeTokenByName(tokenName);
  }

  await expect
    .poll(() => api.hasRequestEvents(site, since), {
      message: `request events of site ${site} in the request explorer (ClickHouse)`,
      timeout: 60_000,
    })
    .toBe(true);
}
