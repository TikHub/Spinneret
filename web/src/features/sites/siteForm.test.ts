import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { EndpointGroupSchema, SiteSchema } from '@/gen/spinneret/v1/site_admin_pb';

import {
  buildCreateGroupRequest,
  buildCreateSiteRequest,
  buildUpdateGroupRequest,
  buildUpdateSiteRequest,
  EMPTY_SITE_FORM,
  groupToForm,
  hasIssues,
  isValidClient,
  removedClients,
  siteToForm,
  validateGroupForm,
  validateSiteForm,
} from './siteForm';

const site = create(SiteSchema, {
  id: 'sit_1',
  name: 'shop',
  displayName: 'Shop',
  description: '',
  clients: ['web', 'app'],
});

describe('site form', () => {
  it('validates names and clients on create', () => {
    expect(validateSiteForm(EMPTY_SITE_FORM, 'create')).toEqual({ name: 'required' });
    expect(validateSiteForm({ ...EMPTY_SITE_FORM, name: 'Shop' }, 'create')).toEqual({ name: 'pattern' });
    expect(validateSiteForm({ ...EMPTY_SITE_FORM, name: 'shop.v2', clients: ['web'] }, 'create')).toEqual({});
    expect(validateSiteForm({ ...EMPTY_SITE_FORM, name: 's', clients: ['Web'] }, 'create')).toEqual({
      clients: 'invalid',
    });
    const many = Array.from({ length: 17 }, (_, i) => `c${i}`);
    expect(validateSiteForm({ ...EMPTY_SITE_FORM, name: 's', clients: many }, 'create').clients).toBe(
      'too_many',
    );
  });

  it('checks client names against ^[a-z0-9_-]{1,32}$', () => {
    expect(isValidClient('mini_app-2')).toBe(true);
    expect(isValidClient('')).toBe(false);
    expect(isValidClient('a'.repeat(33))).toBe(false);
    expect(isValidClient('web app')).toBe(false);
  });

  it('requires a client on edit and ignores the immutable name', () => {
    expect(validateSiteForm({ ...siteToForm(site), name: 'INVALID', clients: [] }, 'edit')).toEqual({
      clients: 'required',
    });
  });

  it('builds create and update requests', () => {
    expect(buildCreateSiteRequest('default', { ...EMPTY_SITE_FORM, name: 'x', displayName: ' X ' })).toEqual({
      namespace: 'default',
      name: 'x',
      displayName: 'X',
      description: '',
      clients: [],
    });
    expect(buildUpdateSiteRequest(site, siteToForm(site))).toEqual({ id: 'sit_1', clients: [] });
    const form = { ...siteToForm(site), description: 'Store', clients: ['web', 'mini'] };
    expect(buildUpdateSiteRequest(site, form)).toEqual({
      id: 'sit_1',
      description: 'Store',
      clients: ['web', 'mini'],
    });
    expect(removedClients(site, form)).toEqual(['app']);
  });
});

describe('endpoint group form', () => {
  const group = create(EndpointGroupSchema, {
    id: 'eg_1',
    client: 'web',
    name: 'search',
    description: 'd',
    lowWatermark: 5,
  });

  it('validates create input', () => {
    const base = { client: 'web', name: 'search', description: '', lowWatermark: '' };
    expect(validateGroupForm(base, 'create')).toEqual({});
    expect(validateGroupForm({ ...base, name: '_default' }, 'create')).toEqual({ name: 'reserved' });
    expect(validateGroupForm({ ...base, name: 'A' }, 'create')).toEqual({ name: 'pattern' });
    expect(validateGroupForm({ ...base, client: '', lowWatermark: '-1' }, 'create')).toEqual({
      client: 'required',
      lowWatermark: 'range',
    });
    expect(hasIssues(validateGroupForm(base, 'create'))).toBe(false);
  });

  it('builds requests with changed fields only', () => {
    expect(
      buildCreateGroupRequest('default', 'shop', {
        client: 'app',
        name: 'feed',
        description: ' ',
        lowWatermark: '3',
      }),
    ).toEqual({
      namespace: 'default',
      site: 'shop',
      client: 'app',
      name: 'feed',
      description: '',
      lowWatermark: 3,
      rules: [],
    });
    expect(buildUpdateGroupRequest(group, groupToForm(group))).toEqual({ id: 'eg_1' });
    expect(buildUpdateGroupRequest(group, { ...groupToForm(group), lowWatermark: '0' })).toEqual({
      id: 'eg_1',
      lowWatermark: 0,
    });
  });
});
