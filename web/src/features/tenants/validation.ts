/** Tenant and namespace slug (CreateTenantRequest.name / CreateNamespaceRequest.name). */
export const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/;
export const MAX_DISPLAY_NAME_LENGTH = 128;
export const MAX_DESCRIPTION_LENGTH = 1024;
export const MAX_OWNER_USER_ID_LENGTH = 64;

/** Kind of entity edited by the slug form. */
export type EntityKind = 'tenant' | 'namespace';
export type EntityFormMode = 'create' | 'edit';

/** Fields of the tenant and namespace forms (the owner only applies to new tenants). */
export interface EntityFormValues {
  name: string;
  displayName: string;
  description: string;
  ownerUserId: string;
}

export const EMPTY_ENTITY_FORM: EntityFormValues = {
  name: '',
  displayName: '',
  description: '',
  ownerUserId: '',
};

export type EntityFormErrors = Partial<{
  name: 'required' | 'pattern';
  displayName: 'tooLong';
  description: 'tooLong';
  ownerUserId: 'tooLong' | 'invalid';
}>;

export function validateEntityForm(
  values: EntityFormValues,
  kind: EntityKind,
  mode: EntityFormMode,
): EntityFormErrors {
  const errors: EntityFormErrors = {};
  if (mode === 'create') {
    if (values.name === '') errors.name = 'required';
    else if (!SLUG_PATTERN.test(values.name)) errors.name = 'pattern';
  }
  if (values.displayName.length > MAX_DISPLAY_NAME_LENGTH) errors.displayName = 'tooLong';
  if (values.description.length > MAX_DESCRIPTION_LENGTH) errors.description = 'tooLong';
  if (kind === 'tenant' && mode === 'create') {
    const owner = values.ownerUserId.trim();
    if (owner.length > MAX_OWNER_USER_ID_LENGTH) errors.ownerUserId = 'tooLong';
    else if (/\s/.test(owner)) errors.ownerUserId = 'invalid';
  }
  return errors;
}

/** Suggests a slug from a display name ("Growth Team" -> "growth-team"). */
export function suggestSlug(displayName: string): string {
  return displayName
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/g, '');
}

/** Update request fields that changed (unset fields keep their value). */
export function entityUpdateInit(
  original: { displayName: string; description: string },
  values: EntityFormValues,
): { displayName?: string; description?: string } {
  const init: { displayName?: string; description?: string } = {};
  if (values.displayName !== original.displayName) init.displayName = values.displayName;
  if (values.description !== original.description) init.description = values.description;
  return init;
}
