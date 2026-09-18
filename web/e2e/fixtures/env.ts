/** Shared environment and constants for the console end-to-end suite. */

/** Base URL of the running stack (Caddy load balancer by default). */
export const BASE_URL = process.env.SPINNERET_UI_URL ?? 'http://localhost:8080';

/** Console administrator used by the suite (deploy/compose/.env). */
export const ADMIN_USER = process.env.SPINNERET_E2E_USER ?? 'admin';
export const ADMIN_PASSWORD = process.env.SPINNERET_E2E_PASSWORD ?? '';

/** Namespace the suite works in. */
export const NAMESPACE = process.env.SPINNERET_E2E_NAMESPACE ?? 'default';

/** Storage state written by auth.setup.ts and reused by every spec. */
export const STORAGE_STATE = 'e2e/.auth/admin.json';

/** Prefix of every resource the suite creates, so leftovers are recognisable. */
export const PREFIX = 'e2e';

/** Site seeded outside the suite that already carries traffic (used for screenshots). */
export const SEEDED_SITE = process.env.SPINNERET_E2E_SEEDED_SITE ?? 'smoke';

let counter = 0;

/**
 * Builds a unique, lowercase, DNS-label-safe resource name. Names must stay
 * stable within a test but never collide between runs.
 */
export function uniqueName(kind: string): string {
  counter += 1;
  const stamp = Date.now().toString(36);
  const rand = Math.random().toString(36).slice(2, 6);
  return `${PREFIX}-${kind}-${stamp}${counter}${rand}`;
}
