import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { checkPermission, PERMISSIONS } from '@/app/auth/permissions';
import { NamespaceAccessSchema, SiteAccessSchema } from '@/gen/spinneret/v1/auth_pb';

import { bindingPermissionSite, scopeLevel } from './selectors';

function scope(namespacePermissions: string[], sitePermissions: string[]) {
  return {
    isPlatformAdmin: false,
    namespace: create(NamespaceAccessSchema, {
      permissions: namespacePermissions,
      sites: [create(SiteAccessSchema, { siteId: 'sit_1', siteName: 'shop', permissions: sitePermissions })],
    }),
  };
}

describe('binding permission checks', () => {
  const publish = PERMISSIONS.policyPublish;

  it('requires a namespace-wide grant for namespace-level bindings', () => {
    const perSite = scope([], [publish]);
    expect(checkPermission(perSite, publish, bindingPermissionSite(''))).toBe(false);
    expect(checkPermission(perSite, publish, bindingPermissionSite('shop'))).toBe(true);
    expect(checkPermission(perSite, publish, bindingPermissionSite('market'))).toBe(false);

    const namespaceWide = scope([publish], []);
    expect(checkPermission(namespaceWide, publish, bindingPermissionSite(''))).toBe(true);
    expect(checkPermission(namespaceWide, publish, bindingPermissionSite('market'))).toBe(true);
  });

  it('derives the binding level from the selected scope', () => {
    expect(scopeLevel({ site: '', client: '', endpointGroup: '' })).toBe('namespace');
    expect(scopeLevel({ site: 'shop', client: '', endpointGroup: '' })).toBe('site');
    expect(scopeLevel({ site: 'shop', client: 'web', endpointGroup: '' })).toBe('client');
    expect(scopeLevel({ site: 'shop', client: 'web', endpointGroup: 'search' })).toBe('endpoint_group');
  });
});
