import { type ConnectError } from '@connectrpc/connect';

import { readStorage, STORAGE_KEYS } from './storage';

/**
 * AuthBridge connects the transport (plain module code) to the auth state held
 * by the application: it supplies the active tenant for the
 * X-Spinneret-Tenant header and receives unauthenticated errors.
 */
export interface AuthBridge {
  getTenantId: () => string | undefined;
  onUnauthenticated: (error: ConnectError) => void;
}

const defaultBridge: AuthBridge = {
  getTenantId: () => readStorage(STORAGE_KEYS.tenant),
  onUnauthenticated: () => undefined,
};

let current: AuthBridge = defaultBridge;

/** Installs the bridge and returns a function restoring the default one. */
export function setAuthBridge(bridge: AuthBridge): () => void {
  current = bridge;
  return () => {
    if (current === bridge) current = defaultBridge;
  };
}

/** Returns the installed bridge. */
export function getAuthBridge(): AuthBridge {
  return current;
}
