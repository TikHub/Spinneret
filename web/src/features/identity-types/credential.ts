import { toJson, type JsonObject, type JsonValue } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';

import { stringifyPayload } from '@/features/identities/payload';
import { type IdentityField } from '@/gen/spinneret/v1/identity_admin_pb';
import { type Credential } from '@/gen/spinneret/v1/lease_pb';

import { DEFAULT_PREVIEW_PAYLOAD } from './examples';

/** Credential segments in delivery order (design doc 5.3). */
export const CREDENTIAL_SEGMENTS = [
  'cookies',
  'cookie_header',
  'headers',
  'query',
  'json',
  'values',
] as const;
export type CredentialSegment = (typeof CREDENTIAL_SEGMENTS)[number];

export interface CredentialView {
  cookies: Array<[string, string]>;
  cookieHeader: string;
  headers: Array<[string, string]>;
  query: Array<[string, string]>;
  /** null when unused. */
  json: JsonValue;
  values: JsonObject;
}

function sortedEntries(map: Readonly<Record<string, string>> | undefined): Array<[string, string]> {
  return Object.entries(map ?? {}).sort(([a], [b]) => a.localeCompare(b));
}

/** Converts a Credential message into plain, sorted segments for display. */
export function credentialView(credential: Credential | undefined): CredentialView {
  return {
    cookies: sortedEntries(credential?.cookies),
    cookieHeader: credential?.cookieHeader ?? '',
    headers: sortedEntries(credential?.headers),
    query: sortedEntries(credential?.query),
    json: credential?.json ? toJson(ValueSchema, credential.json) : null,
    values: credential?.values ?? {},
  };
}

/** Whether a segment carries data. */
export function isSegmentUsed(view: CredentialView, segment: CredentialSegment): boolean {
  switch (segment) {
    case 'cookies':
      return view.cookies.length > 0;
    case 'cookie_header':
      return view.cookieHeader !== '';
    case 'headers':
      return view.headers.length > 0;
    case 'query':
      return view.query.length > 0;
    case 'json':
      return view.json !== null;
    case 'values':
      return Object.keys(view.values).length > 0;
    default:
      return false;
  }
}

/** Example value per field type, used to prefill the preview payload. */
function sampleValue(field: Pick<IdentityField, 'name' | 'type'>): JsonValue {
  switch (field.type) {
    case 'cookie_map':
      return { sessionid: 'example-session', csrf_token: 'example-csrf' };
    case 'number':
      return 0;
    case 'bool':
      return false;
    case 'json':
      return {};
    case 'secret_ref':
      return `path/to/${field.name}`;
    default:
      return `example-${field.name}`;
  }
}

/** Sample payload containing every field of an identity type. */
export function samplePayload(fields: readonly Pick<IdentityField, 'name' | 'type'>[]): JsonObject {
  const out: JsonObject = {};
  for (const field of fields) out[field.name] = sampleValue(field);
  return out;
}

/** Initial sample payload for a type's fields (or an empty object). */
export function initialPreviewPayload(fields: readonly IdentityField[] | undefined): string {
  return fields && fields.length > 0 ? stringifyPayload(samplePayload(fields)) : DEFAULT_PREVIEW_PAYLOAD;
}
