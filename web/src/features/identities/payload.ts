import { type JsonObject, type JsonValue } from '@bufbuild/protobuf';

/** Prefix the server uses for masked sensitive values ("••••" + last 4). */
export const MASK_PREFIX = '••••';

/** Maximum payload size accepted by the editor (1 MiB, as PreviewDelivery). */
export const MAX_PAYLOAD_BYTES = 1_048_576;

/** Whether any string inside the value is a masked placeholder. */
export function containsMaskedValues(value: JsonValue | undefined): boolean {
  if (typeof value === 'string') return value.startsWith(MASK_PREFIX);
  if (Array.isArray(value)) return value.some((item) => containsMaskedValues(item));
  if (value !== null && typeof value === 'object') {
    return Object.values(value).some((item) => containsMaskedValues(item));
  }
  return false;
}

/** Top-level keys whose values contain masked placeholders. */
export function maskedFieldNames(payload: JsonObject | undefined): string[] {
  if (!payload) return [];
  return Object.entries(payload)
    .filter(([, value]) => containsMaskedValues(value))
    .map(([key]) => key)
    .sort();
}

export type PayloadParseResult =
  | { ok: true; value: JsonObject }
  | { ok: false; error: 'empty' | 'invalidJson' | 'notObject' | 'tooLarge'; detail?: string };

function isJsonObject(value: unknown): value is JsonObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/** Parses editor text into a JSON object payload. */
export function parsePayloadJson(text: string): PayloadParseResult {
  if (text.trim() === '') return { ok: false, error: 'empty' };
  if (new TextEncoder().encode(text).length > MAX_PAYLOAD_BYTES) return { ok: false, error: 'tooLarge' };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    return { ok: false, error: 'invalidJson', detail: err instanceof Error ? err.message : String(err) };
  }
  if (!isJsonObject(parsed)) return { ok: false, error: 'notObject' };
  return { ok: true, value: parsed };
}

/** Pretty JSON for editors. */
export function stringifyPayload(value: JsonValue | undefined): string {
  return JSON.stringify(value ?? {}, null, 2);
}
