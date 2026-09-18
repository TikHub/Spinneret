import { type EndpointGroup, type Site } from '@/gen/spinneret/v1/site_admin_pb';

/** Site and endpoint group names (internal/sitesvc namePattern). */
export const NAME_PATTERN = /^[a-z0-9_][a-z0-9_.-]{0,63}$/;
/** Client types (internal/sitesvc clientPattern). */
export const CLIENT_PATTERN = /^[a-z0-9_-]{1,32}$/;

export const SITE_LIMITS = {
  clients: 16,
  displayName: 128,
  description: 1024,
  lowWatermark: 2_147_483_647,
} as const;

/** Clients of a site created without clients. */
export const DEFAULT_CLIENTS = ['web'] as const;

/** Character count as the backend counts it (runes). */
export function charLength(value: string): number {
  return [...value].length;
}

export function isValidClient(value: string): boolean {
  return CLIENT_PATTERN.test(value);
}

export interface SiteForm {
  name: string;
  displayName: string;
  description: string;
  clients: string[];
}

export const EMPTY_SITE_FORM: SiteForm = { name: '', displayName: '', description: '', clients: [] };

export function siteToForm(site: Site): SiteForm {
  return {
    name: site.name,
    displayName: site.displayName,
    description: site.description,
    clients: [...site.clients],
  };
}

export interface SiteFormIssues {
  name?: 'required' | 'pattern';
  displayName?: 'too_long';
  description?: 'too_long';
  clients?: 'required' | 'too_many' | 'invalid' | 'duplicate';
}

/** Validates the site dialog; on edit the name is immutable and at least one client is required. */
export function validateSiteForm(form: SiteForm, mode: 'create' | 'edit'): SiteFormIssues {
  const issues: SiteFormIssues = {};
  if (mode === 'create') {
    if (form.name === '') issues.name = 'required';
    else if (!NAME_PATTERN.test(form.name)) issues.name = 'pattern';
  }
  if (charLength(form.displayName.trim()) > SITE_LIMITS.displayName) issues.displayName = 'too_long';
  if (charLength(form.description.trim()) > SITE_LIMITS.description) issues.description = 'too_long';
  if (mode === 'edit' && form.clients.length === 0) issues.clients = 'required';
  else if (form.clients.length > SITE_LIMITS.clients) issues.clients = 'too_many';
  else if (form.clients.some((c) => !isValidClient(c))) issues.clients = 'invalid';
  else if (new Set(form.clients).size !== form.clients.length) issues.clients = 'duplicate';
  return issues;
}

export function buildCreateSiteRequest(namespace: string, form: SiteForm) {
  return {
    namespace,
    name: form.name,
    displayName: form.displayName.trim(),
    description: form.description.trim(),
    clients: [...form.clients],
  };
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/** UpdateSite request with only the changed fields (clients sent as a complete list when changed). */
export function buildUpdateSiteRequest(site: Site, form: SiteForm) {
  const req: { id: string; displayName?: string; description?: string; clients: string[] } = {
    id: site.id,
    clients: [],
  };
  if (form.displayName.trim() !== site.displayName) req.displayName = form.displayName.trim();
  if (form.description.trim() !== site.description) req.description = form.description.trim();
  if (!sameList(form.clients, site.clients)) req.clients = [...form.clients];
  return req;
}

/** Clients present on the site but missing from the form (their removal fails while identities exist). */
export function removedClients(site: Site, form: SiteForm): string[] {
  return site.clients.filter((client) => !form.clients.includes(client));
}

export interface GroupForm {
  client: string;
  name: string;
  description: string;
  lowWatermark: string;
}

export function groupToForm(group: EndpointGroup): GroupForm {
  return {
    client: group.client,
    name: group.name,
    description: group.description,
    lowWatermark: String(group.lowWatermark),
  };
}

export interface GroupFormIssues {
  client?: 'required';
  name?: 'required' | 'pattern' | 'reserved';
  description?: 'too_long';
  lowWatermark?: 'range';
}

export function parseLowWatermark(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed === '') return 0;
  if (!/^\d+$/.test(trimmed)) return undefined;
  const n = Number(trimmed);
  return n <= SITE_LIMITS.lowWatermark ? n : undefined;
}

export function validateGroupForm(form: GroupForm, mode: 'create' | 'edit'): GroupFormIssues {
  const issues: GroupFormIssues = {};
  if (mode === 'create') {
    if (form.client === '') issues.client = 'required';
    if (form.name === '') issues.name = 'required';
    else if (form.name === '_default') issues.name = 'reserved';
    else if (!NAME_PATTERN.test(form.name)) issues.name = 'pattern';
  }
  if (charLength(form.description.trim()) > SITE_LIMITS.description) issues.description = 'too_long';
  if (parseLowWatermark(form.lowWatermark) === undefined) issues.lowWatermark = 'range';
  return issues;
}

export function buildCreateGroupRequest(namespace: string, site: string, form: GroupForm) {
  return {
    namespace,
    site,
    client: form.client,
    name: form.name,
    description: form.description.trim(),
    lowWatermark: parseLowWatermark(form.lowWatermark) ?? 0,
    rules: [],
  };
}

export function buildUpdateGroupRequest(group: EndpointGroup, form: GroupForm) {
  const req: { id: string; description?: string; lowWatermark?: number } = { id: group.id };
  if (form.description.trim() !== group.description) req.description = form.description.trim();
  const lowWatermark = parseLowWatermark(form.lowWatermark);
  if (lowWatermark !== undefined && lowWatermark !== group.lowWatermark) req.lowWatermark = lowWatermark;
  return req;
}

export function hasIssues(issues: object): boolean {
  return Object.values(issues).some((v) => v !== undefined);
}
