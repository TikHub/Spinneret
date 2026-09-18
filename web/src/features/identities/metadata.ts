import { type MessageInitShape } from '@bufbuild/protobuf';

import { type Identity, type UpdateIdentityRequestSchema } from '@/gen/spinneret/v1/identity_admin_pb';
import { duplicateKeys, pairsToRecord, recordToPairs, type KeyValuePair } from '@/lib/keyValue';

export const METADATA_LIMITS = {
  region: 64,
  tagLength: 64,
  tags: 64,
  labels: 64,
  labelKey: 64,
  labelValue: 256,
  accountRef: 256,
} as const;

export interface MetadataForm {
  region: string;
  tags: string[];
  labels: KeyValuePair[];
  accountRef: string;
}

export function metadataFormFromIdentity(identity: Identity): MetadataForm {
  return {
    region: identity.region,
    tags: [...identity.tags],
    labels: recordToPairs(identity.labels),
    accountRef: identity.accountRef,
  };
}

export interface MetadataErrors {
  region?: 'tooLong';
  tags?: 'tooMany';
  labels?: 'duplicate' | 'tooMany' | 'keyTooLong' | 'valueTooLong' | 'emptyKey';
  accountRef?: 'tooLong';
}

export function validateMetadata(form: MetadataForm): MetadataErrors {
  const errors: MetadataErrors = {};
  if (form.region.trim().length > METADATA_LIMITS.region) errors.region = 'tooLong';
  if (form.tags.length > METADATA_LIMITS.tags) errors.tags = 'tooMany';
  const filled = form.labels.filter((p) => p.key.trim() !== '' || p.value !== '');
  if (filled.some((p) => p.key.trim() === '')) errors.labels = 'emptyKey';
  else if (duplicateKeys(filled).size > 0) errors.labels = 'duplicate';
  else if (filled.length > METADATA_LIMITS.labels) errors.labels = 'tooMany';
  else if (filled.some((p) => p.key.trim().length > METADATA_LIMITS.labelKey)) errors.labels = 'keyTooLong';
  else if (filled.some((p) => p.value.length > METADATA_LIMITS.labelValue)) errors.labels = 'valueTooLong';
  if (form.accountRef.trim().length > METADATA_LIMITS.accountRef) errors.accountRef = 'tooLong';
  return errors;
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

function sameRecord(a: Readonly<Record<string, string>>, b: Readonly<Record<string, string>>): boolean {
  const keysA = Object.keys(a).sort();
  const keysB = Object.keys(b).sort();
  return sameList(keysA, keysB) && keysA.every((k) => a[k] === b[k]);
}

/**
 * Builds an UpdateIdentity request containing only changed attributes, or
 * undefined when nothing changed.
 */
export function buildUpdateIdentityRequest(
  identity: Identity,
  form: MetadataForm,
): MessageInitShape<typeof UpdateIdentityRequestSchema> | undefined {
  const region = form.region.trim();
  const tags = form.tags.map((t) => t.trim()).filter(Boolean);
  const labels = pairsToRecord(form.labels);
  const accountRef = form.accountRef.trim();

  const request: MessageInitShape<typeof UpdateIdentityRequestSchema> = { id: identity.id };
  let changed = false;
  if (region !== identity.region) {
    request.region = region;
    changed = true;
  }
  if (!sameList(tags, identity.tags)) {
    request.tags = tags;
    request.setTags = true;
    changed = true;
  }
  if (!sameRecord(labels, identity.labels)) {
    request.labels = labels;
    request.setLabels = true;
    changed = true;
  }
  if (accountRef !== identity.accountRef) {
    request.accountRef = accountRef;
    changed = true;
  }
  return changed ? request : undefined;
}
