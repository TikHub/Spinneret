import { type IdentityField } from '@/gen/spinneret/v1/identity_admin_pb';

/**
 * Keys the JSON Lines import format reserves for metadata. The server rejects
 * a type whose field is named after one, but a type created before that check
 * would silently have its field swallowed as metadata, so the form refuses it
 * rather than sending a row that means something else than it looks like.
 */
const RESERVED_KEYS = new Set(['payload', '_account', '_region', '_tags', '_labels']);

/** Reserved metadata carried alongside the payload. */
export interface NewIdentityMeta {
  /** External account reference (`_account`). */
  account: string;
  /** Region (`_region`). */
  region: string;
  /** Comma-separated tags (`_tags`). */
  tags: string;
}

/** Raw text of every field, keyed by field name. */
export type FieldValues = Record<string, string>;

/** Why a field value cannot be turned into a payload value. */
export type FieldError = 'required' | 'notANumber' | 'invalidJson' | 'reserved';

export type NewIdentityRow = { ok: true; line: string } | { ok: false; errors: Record<string, FieldError> };

/** A blank form for the fields of an identity type. */
export function emptyFieldValues(fields: readonly IdentityField[]): FieldValues {
  return Object.fromEntries(fields.map((f) => [f.name, '']));
}

/**
 * Turns the form into a single JSON Lines row for ImportIdentities.
 *
 * A one-row import is how a single identity is created: there is no
 * CreateIdentity RPC, and routing the form through the import path means the
 * value coercion, deduplication and activation rules are the ones that already
 * apply to bulk imports rather than a second set that could drift from them.
 */
export function buildNewIdentityRow(
  fields: readonly IdentityField[],
  values: FieldValues,
  meta: NewIdentityMeta,
): NewIdentityRow {
  const row: Record<string, unknown> = {};
  const errors: Record<string, FieldError> = {};

  for (const field of fields) {
    if (RESERVED_KEYS.has(field.name)) {
      errors[field.name] = 'reserved';
      continue;
    }
    const text = (values[field.name] ?? '').trim();

    // A boolean is always one of two values, so a blank switch is `false`
    // rather than a missing field.
    if (field.type === 'bool') {
      row[field.name] = text === 'true';
      continue;
    }
    if (text === '') {
      if (field.required) errors[field.name] = 'required';
      continue;
    }
    const coerced = coerce(field.type, text);
    if (!coerced.ok) {
      errors[field.name] = coerced.error;
      continue;
    }
    row[field.name] = coerced.value;
  }

  if (Object.keys(errors).length > 0) return { ok: false, errors };

  const account = meta.account.trim();
  const region = meta.region.trim();
  const tags = parseTags(meta.tags);
  if (account !== '') row._account = account;
  if (region !== '') row._region = region;
  if (tags.length > 0) row._tags = tags;

  // JSON.stringify escapes newlines, so a pasted multi-line value stays one row.
  return { ok: true, line: JSON.stringify(row) };
}

type Coerced = { ok: true; value: unknown } | { ok: false; error: FieldError };

/**
 * Coerces one trimmed value to its payload type. The result is a discriminated
 * union rather than a sentinel value, because a `string` field is allowed to
 * hold the very text an error code would use.
 */
function coerce(type: string, text: string): Coerced {
  switch (type) {
    case 'number': {
      const n = Number(text);
      return Number.isFinite(n) ? { ok: true, value: n } : { ok: false, error: 'notANumber' };
    }
    case 'json': {
      const parsed = tryParseJson(text);
      return parsed.ok ? { ok: true, value: parsed.value } : { ok: false, error: 'invalidJson' };
    }
    case 'cookie_map': {
      // cookie_map accepts a Cookie header string, an object or a browser
      // export array. Anything that is valid JSON is passed on as JSON; the
      // rest stays the header string it looks like.
      if (!text.startsWith('{') && !text.startsWith('[')) return { ok: true, value: text };
      const parsed = tryParseJson(text);
      return { ok: true, value: parsed.ok ? parsed.value : text };
    }
    default:
      return { ok: true, value: text };
  }
}

function tryParseJson(text: string): { ok: true; value: unknown } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch {
    return { ok: false };
  }
}

/** Splits the tag input on commas, dropping blanks and duplicates. */
function parseTags(input: string): string[] {
  const seen = new Set<string>();
  for (const tag of input.split(',')) {
    const trimmed = tag.trim();
    if (trimmed !== '') seen.add(trimmed);
  }
  return [...seen];
}
