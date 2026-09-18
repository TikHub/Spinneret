import { type MessageInitShape } from '@bufbuild/protobuf';

import {
  type CreateSecretRequestSchema,
  type SecretInfo,
  type UpdateSecretRequestSchema,
} from '@/gen/spinneret/v1/secret_admin_pb';
import { toDate, toTimestamp } from '@/lib/time';

import { isValidSecretTag, validateSecretPath, type SecretPathError } from './secretPath';
import { parseDateTimeLocal, toDateTimeLocal } from './secretTime';

export const MAX_SECRET_VALUE_BYTES = 64 * 1024;
export const MAX_SECRET_DESCRIPTION_LENGTH = 1024;
export const MAX_SECRET_TAGS = 64;

/** Values of the create/update secret form. */
export interface SecretFormValues {
  path: string;
  /** Plaintext value; on update an empty value keeps the current version. */
  value: string;
  description: string;
  tags: string[];
  expires: boolean;
  /** datetime-local value. */
  expiresAt: string;
}

export const EMPTY_SECRET_FORM: SecretFormValues = {
  path: '',
  value: '',
  description: '',
  tags: [],
  expires: false,
  expiresAt: '',
};

export function formFromSecret(secret: SecretInfo): SecretFormValues {
  const expiresAt = toDateTimeLocal(secret.expiresAt);
  return {
    path: secret.path,
    value: '',
    description: secret.description,
    tags: [...secret.tags],
    expires: expiresAt !== '',
    expiresAt,
  };
}

export function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length;
}

export interface SecretFormErrors {
  path?: SecretPathError;
  value?: 'required' | 'tooLarge';
  tags?: 'invalid' | 'limit';
  expiresAt?: 'required' | 'invalid' | 'past';
}

/** Validates the form; `mode` "edit" makes the value optional and the path fixed. */
export function validateSecretForm(
  values: SecretFormValues,
  mode: 'create' | 'edit',
  now: number,
  initial?: SecretFormValues,
): SecretFormErrors {
  const errors: SecretFormErrors = {};
  if (mode === 'create') {
    const path = validateSecretPath(values.path);
    if (path) errors.path = path;
  }
  if (values.value === '') {
    if (mode === 'create') errors.value = 'required';
  } else if (utf8Bytes(values.value) > MAX_SECRET_VALUE_BYTES) {
    errors.value = 'tooLarge';
  }
  if (values.tags.length > MAX_SECRET_TAGS) errors.tags = 'limit';
  else if (!values.tags.every(isValidSecretTag)) errors.tags = 'invalid';
  if (values.expires) {
    const unchanged = initial?.expires === true && initial.expiresAt === values.expiresAt;
    const date = parseDateTimeLocal(values.expiresAt);
    if (values.expiresAt === '') errors.expiresAt = 'required';
    else if (!date) errors.expiresAt = 'invalid';
    else if (!unchanged && date.getTime() <= now) errors.expiresAt = 'past';
  }
  return errors;
}

export function hasErrors(errors: SecretFormErrors): boolean {
  return Object.values(errors).some((v) => v !== undefined);
}

/** CreateSecret request from form values. */
export function createSecretRequest(
  namespace: string,
  values: SecretFormValues,
): MessageInitShape<typeof CreateSecretRequestSchema> {
  const expires = values.expires ? parseDateTimeLocal(values.expiresAt) : undefined;
  return {
    namespace,
    path: values.path,
    value: values.value,
    description: values.description.trim(),
    tags: values.tags,
    ...(expires ? { expiresAt: toTimestamp(expires) } : {}),
  };
}

function sameTags(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((tag, i) => tag === b[i]);
}

/**
 * UpdateSecret request with only the changed fields: a non-empty value stores
 * a new version, tags are replaced only when they changed, and the expiry is
 * set or cleared only when it changed. Returns undefined when nothing changed.
 */
export function updateSecretRequest(
  secret: SecretInfo,
  values: SecretFormValues,
): MessageInitShape<typeof UpdateSecretRequestSchema> | undefined {
  const request: MessageInitShape<typeof UpdateSecretRequestSchema> = { id: secret.id };
  let changed = false;
  if (values.value !== '') {
    request.value = values.value;
    changed = true;
  }
  const description = values.description.trim();
  if (description !== secret.description) {
    request.description = description;
    changed = true;
  }
  if (!sameTags(values.tags, secret.tags)) {
    request.tags = values.tags;
    request.setTags = true;
    changed = true;
  }
  const before = toDate(secret.expiresAt);
  const after = values.expires ? parseDateTimeLocal(values.expiresAt) : undefined;
  if (!after && before) {
    request.clearExpiresAt = true;
    changed = true;
  } else if (after && toDateTimeLocal(before) !== values.expiresAt) {
    request.expiresAt = toTimestamp(after);
    changed = true;
  }
  return changed ? request : undefined;
}
