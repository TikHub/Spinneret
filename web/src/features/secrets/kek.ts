import { type GetKEKStatusResponse, type KEKInfo } from '@/gen/spinneret/v1/secret_admin_pb';
import { toNumber } from '@/lib/format';

/** Records still wrapped with a KEK other than the current one. */
export function pendingRewrapRecords(status: GetKEKStatusResponse): number {
  return status.keks.filter((k) => !k.current).reduce((sum, k) => sum + toNumber(k.wrappedRecords), 0);
}

/** KEKs that still wrap records but are not configured (those records cannot be decrypted). */
export function missingKeks(status: GetKEKStatusResponse): KEKInfo[] {
  return status.keks.filter((k) => !k.configured && toNumber(k.wrappedRecords) > 0);
}

/** Progress of the running or last rewrap job; ratio is 0..1. */
export function kekProgress(status: GetKEKStatusResponse): { done: number; total: number; ratio: number } {
  const done = toNumber(status.rewrapDone);
  const total = toNumber(status.rewrapTotal);
  if (total > 0) return { done, total, ratio: Math.min(1, done / total) };
  return { done, total, ratio: status.rewrapRunning ? 0 : 1 };
}
