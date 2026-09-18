import { type User } from '@/gen/spinneret/v1/auth_pb';

/** Roles of implementation spec section 3.2, least privileged first. */
export const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
export type Role = (typeof ROLES)[number];

/** Permissions a binding may add on top of its role. */
export const EXTRA_PERMISSION_OPTIONS = [
  'config:publish',
  'secret:reveal',
  'identity:reveal',
  'policy:publish',
] as const;
export type ExtraPermission = (typeof EXTRA_PERMISSION_OPTIONS)[number];

export const USERNAME_PATTERN = /^[a-z0-9][a-z0-9._-]{2,63}$/;
export const MIN_PASSWORD_LENGTH = 10;
export const MAX_PASSWORD_LENGTH = 1024;
export const MAX_DISPLAY_NAME_LENGTH = 128;
export const MAX_EMAIL_LENGTH = 254;
export const MAX_BINDING_SITES = 500;
/** Pragmatic email check; the server validates strictly. */
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
/** BCP 47 locale (UpdateUserRequest.locale). */
export const LOCALE_PATTERN = /^([A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*)?$/;

export function isRole(value: string): value is Role {
  return (ROLES as readonly string[]).includes(value);
}

export function isExtraPermission(value: string): value is ExtraPermission {
  return (EXTRA_PERMISSION_OPTIONS as readonly string[]).includes(value);
}

/** Role binding fields shared by the create user and add binding forms. */
export interface BindingFormValues {
  role: Role;
  /** Namespace name; empty = all namespaces. */
  namespace: string;
  /** Site names; only allowed together with a namespace. */
  sites: string[];
  extraPermissions: ExtraPermission[];
}

export const EMPTY_BINDING: BindingFormValues = {
  role: 'viewer',
  namespace: '',
  sites: [],
  extraPermissions: [],
};

export type BindingErrors = Partial<{
  role: 'invalid';
  sites: 'requireNamespace' | 'tooMany' | 'duplicate';
  extraPermissions: 'invalid';
}>;

/** Validates binding fields (mirrors the sites_require_namespace rule). */
export function validateBinding(values: BindingFormValues): BindingErrors {
  const errors: BindingErrors = {};
  if (!isRole(values.role)) errors.role = 'invalid';
  if (values.sites.length > 0) {
    if (values.namespace.trim() === '') errors.sites = 'requireNamespace';
    else if (values.sites.length > MAX_BINDING_SITES) errors.sites = 'tooMany';
    else if (new Set(values.sites).size !== values.sites.length) errors.sites = 'duplicate';
  }
  if (!values.extraPermissions.every(isExtraPermission)) errors.extraPermissions = 'invalid';
  if (new Set(values.extraPermissions).size !== values.extraPermissions.length) {
    errors.extraPermissions = 'invalid';
  }
  return errors;
}

/** Changes the namespace; site restrictions belong to one namespace and are cleared. */
export function withNamespace(values: BindingFormValues, namespace: string): BindingFormValues {
  if (values.namespace === namespace) return values;
  return { ...values, namespace, sites: [] };
}

/** Request fields of a binding (CreateRoleBinding / CreateUser). */
export function toBindingInit(values: BindingFormValues): {
  role: Role;
  namespace: string;
  sites: string[];
  extraPermissions: string[];
} {
  const namespace = values.namespace.trim();
  return {
    role: values.role,
    namespace,
    sites: namespace === '' ? [] : [...values.sites],
    extraPermissions: [...values.extraPermissions],
  };
}

/** Extra permissions the role already grants (shown as a hint). */
export function redundantExtraPermissions(
  role: Role,
  extras: readonly ExtraPermission[],
  rolePermissions: Readonly<Record<string, ReadonlySet<string>>>,
): ExtraPermission[] {
  const granted = rolePermissions[role];
  return granted ? extras.filter((p) => granted.has(p)) : [];
}

export type PasswordError = 'required' | 'tooShort' | 'tooLong';

export function validatePassword(password: string): PasswordError | undefined {
  if (password === '') return 'required';
  if (password.length < MIN_PASSWORD_LENGTH) return 'tooShort';
  if (password.length > MAX_PASSWORD_LENGTH) return 'tooLong';
  return undefined;
}

/**
 * Rough password strength from 0 (very weak) to 4 (strong): length and the
 * variety of character classes. It is a hint only; the server enforces the
 * minimum length.
 */
export function passwordStrength(password: string): 0 | 1 | 2 | 3 | 4 {
  if (password.length < MIN_PASSWORD_LENGTH) return 0;
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((re) => re.test(password)).length;
  const repeated = /^(.)\1+$/.test(password);
  if (repeated) return 0;
  let score = 1;
  if (classes >= 2) score += 1;
  if (classes >= 3) score += 1;
  if (password.length >= 16 && classes >= 2) score += 1;
  return Math.min(score, 4) as 0 | 1 | 2 | 3 | 4;
}

export type EmailError = 'invalid' | 'tooLong';

export function validateEmail(email: string): EmailError | undefined {
  const value = email.trim();
  if (value === '') return undefined;
  if (value.length > MAX_EMAIL_LENGTH) return 'tooLong';
  return EMAIL_PATTERN.test(value) ? undefined : 'invalid';
}

export interface CreateUserValues {
  username: string;
  displayName: string;
  email: string;
  password: string;
  binding: BindingFormValues;
}

export const EMPTY_CREATE_USER: CreateUserValues = {
  username: '',
  displayName: '',
  email: '',
  password: '',
  binding: EMPTY_BINDING,
};

export type CreateUserErrors = Partial<{
  username: 'required' | 'pattern';
  displayName: 'tooLong';
  email: EmailError;
  password: PasswordError;
}> &
  BindingErrors;

export function validateCreateUser(values: CreateUserValues): CreateUserErrors {
  const errors: CreateUserErrors = { ...validateBinding(values.binding) };
  if (values.username === '') errors.username = 'required';
  else if (!USERNAME_PATTERN.test(values.username)) errors.username = 'pattern';
  if (values.displayName.length > MAX_DISPLAY_NAME_LENGTH) errors.displayName = 'tooLong';
  const email = validateEmail(values.email);
  if (email) errors.email = email;
  const password = validatePassword(values.password);
  if (password) errors.password = password;
  return errors;
}

/** Editable profile fields of a user. */
export interface EditUserValues {
  displayName: string;
  email: string;
  /** "" = browser default. */
  locale: string;
  disabled: boolean;
}

export function editValuesFromUser(user: User): EditUserValues {
  return {
    displayName: user.displayName,
    email: user.email,
    locale: user.locale,
    disabled: user.disabled,
  };
}

export type EditUserErrors = Partial<{
  displayName: 'tooLong';
  email: EmailError;
  locale: 'invalid';
}>;

export function validateEditUser(values: EditUserValues): EditUserErrors {
  const errors: EditUserErrors = {};
  if (values.displayName.length > MAX_DISPLAY_NAME_LENGTH) errors.displayName = 'tooLong';
  const email = validateEmail(values.email);
  if (email) errors.email = email;
  if (!LOCALE_PATTERN.test(values.locale)) errors.locale = 'invalid';
  return errors;
}

/** UpdateUserRequest fields that changed (unset fields keep their value on the server). */
export function userUpdateInit(
  user: User,
  values: EditUserValues,
): { id: string; displayName?: string; email?: string; locale?: string; disabled?: boolean } {
  const init: { id: string; displayName?: string; email?: string; locale?: string; disabled?: boolean } = {
    id: user.id,
  };
  if (values.displayName !== user.displayName) init.displayName = values.displayName;
  if (values.email.trim() !== user.email) init.email = values.email.trim();
  if (values.locale !== user.locale) init.locale = values.locale;
  if (values.disabled !== user.disabled) init.disabled = values.disabled;
  return init;
}

/** Reports whether the update changes anything besides the ID. */
export function hasUserChanges(init: ReturnType<typeof userUpdateInit>): boolean {
  return Object.keys(init).length > 1;
}
