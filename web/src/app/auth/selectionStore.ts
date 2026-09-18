import { readStorage, STORAGE_KEYS, writeStorage } from '@/lib/storage';

/** The user's stored tenant/namespace preference (may be stale; see resolveSelection). */
export interface StoredSelection {
  tenantId: string | undefined;
  namespace: string | undefined;
}

type Listener = () => void;

let snapshot: StoredSelection = {
  tenantId: readStorage(STORAGE_KEYS.tenant),
  namespace: readStorage(STORAGE_KEYS.namespace),
};
const listeners = new Set<Listener>();

function emit(next: StoredSelection): void {
  snapshot = next;
  writeStorage(STORAGE_KEYS.tenant, next.tenantId);
  writeStorage(STORAGE_KEYS.namespace, next.namespace);
  listeners.forEach((listener) => listener());
}

/**
 * External store for the active tenant/namespace preference, persisted in
 * localStorage (spinneret.tenant, spinneret.namespace). Shared by React
 * (useSyncExternalStore) and the transport (tenant header).
 */
export const selectionStore = {
  get(): StoredSelection {
    return snapshot;
  },
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  setTenant(tenantId: string, namespace?: string): void {
    emit({ tenantId, namespace });
  },
  setNamespace(namespace: string): void {
    emit({ ...snapshot, namespace });
  },
  /** Stores the resolved selection when it differs from the stored one. */
  sync(tenantId: string | undefined, namespace: string | undefined): void {
    if (snapshot.tenantId === tenantId && snapshot.namespace === namespace) return;
    emit({ tenantId, namespace });
  },
};
