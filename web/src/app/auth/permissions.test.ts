import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import {
  NamespaceAccessSchema,
  NamespaceSchema,
  RoleBindingSchema,
  SiteAccessSchema,
  TenantAccessSchema,
  TenantSchema,
} from '@/gen/spinneret/v1/auth_pb';

import {
  bindingGrants,
  checkPermission,
  checkTenantPermission,
  resolveSelection,
  sitesWithPermission,
} from './permissions';

const namespace = create(NamespaceAccessSchema, {
  namespace: create(NamespaceSchema, { id: 'ns_1', name: 'default' }),
  permissions: ['site:read', 'dashboard:read', 'config:read'],
  sites: [
    create(SiteAccessSchema, {
      siteId: 'sit_a',
      siteName: 'shop-a',
      permissions: ['identity:read', 'identity:operate', 'proxy:read', 'config:publish'],
    }),
    create(SiteAccessSchema, { siteId: 'sit_b', siteName: 'shop-b', permissions: ['identity:read'] }),
  ],
});

describe('checkPermission', () => {
  const scope = { isPlatformAdmin: false, namespace };

  it('grants namespace-wide permissions for any site', () => {
    expect(checkPermission(scope, 'site:read')).toBe(true);
    expect(checkPermission(scope, 'site:read', 'sit_zzz')).toBe(true);
  });

  it('grants per-site permissions only for that site (by ID or name)', () => {
    expect(checkPermission(scope, 'identity:operate', 'sit_a')).toBe(true);
    expect(checkPermission(scope, 'identity:operate', 'shop-a')).toBe(true);
    expect(checkPermission(scope, 'identity:operate', 'sit_b')).toBe(false);
    expect(checkPermission(scope, 'identity:read', 'shop-b')).toBe(true);
  });

  it('without a site, per-site grants on any site count', () => {
    expect(checkPermission(scope, 'identity:read')).toBe(true);
    expect(checkPermission(scope, 'identity:write')).toBe(false);
    expect(checkPermission(scope, 'proxy:read')).toBe(true);
  });

  it('never derives namespace-level permissions from per-site grants', () => {
    expect(checkPermission(scope, 'config:publish')).toBe(false);
    expect(checkPermission(scope, 'config:publish', 'sit_a')).toBe(false);
  });

  it('reserves platform permissions for platform admins', () => {
    expect(checkPermission(scope, 'tenant:manage')).toBe(false);
    expect(checkPermission({ isPlatformAdmin: true, namespace: undefined }, 'tenant:manage')).toBe(true);
    expect(checkPermission({ isPlatformAdmin: true, namespace: undefined }, 'secret:reveal', 'sit_a')).toBe(
      true,
    );
  });

  it('denies everything without a namespace', () => {
    expect(checkPermission({ isPlatformAdmin: false, namespace: undefined }, 'site:read')).toBe(false);
  });

  it('lists sites with per-site grants', () => {
    expect(sitesWithPermission(scope, 'identity:read')).toEqual(['sit_a', 'sit_b']);
    expect(sitesWithPermission(scope, 'site:read')).toEqual([]);
  });
});

describe('bindingGrants', () => {
  it('uses the role table and honours only valid extra permissions', () => {
    const viewer = create(RoleBindingSchema, {
      role: 'viewer',
      extraPermissions: ['config:publish', 'user:write'],
    });
    expect(bindingGrants(viewer, 'dashboard:read')).toBe(true);
    expect(bindingGrants(viewer, 'token:read')).toBe(false);
    expect(bindingGrants(viewer, 'config:publish')).toBe(true);
    expect(bindingGrants(viewer, 'user:write')).toBe(false);
    expect(bindingGrants(create(RoleBindingSchema, { role: 'owner' }), 'user:read')).toBe(true);
    expect(bindingGrants(create(RoleBindingSchema, { role: 'root' }), 'site:read')).toBe(false);
  });
});

describe('checkPermission with bindings', () => {
  const siteOnly = create(NamespaceAccessSchema, {
    namespace: create(NamespaceSchema, { id: 'ns_1', name: 'default' }),
    permissions: [],
    sites: [create(SiteAccessSchema, { siteId: 'sit_a', permissions: ['proxy:read', 'namespace:read'] })],
  });

  it('grants proxy:read and namespace:read from site-restricted bindings pinned to the namespace', () => {
    const pinned = create(RoleBindingSchema, { role: 'admin', namespaceId: 'ns_1', siteIds: ['sit_a'] });
    const scope = { isPlatformAdmin: false, namespace: siteOnly, bindings: [pinned] };
    expect(checkPermission(scope, 'proxy:read')).toBe(true);
    expect(checkPermission(scope, 'namespace:read')).toBe(true);
    expect(checkPermission(scope, 'proxy:write')).toBe(false);
  });

  it('denies them for site-restricted bindings covering every namespace', () => {
    const tenantWide = create(RoleBindingSchema, { role: 'admin', namespaceId: '', siteIds: ['sit_a'] });
    const scope = { isPlatformAdmin: false, namespace: siteOnly, bindings: [tenantWide] };
    expect(checkPermission(scope, 'proxy:read')).toBe(false);
    expect(checkPermission(scope, 'namespace:read')).toBe(false);
  });
});

describe('checkTenantPermission', () => {
  const tenantWith = (...bindings: MessageInitShape<typeof RoleBindingSchema>[]) =>
    create(TenantAccessSchema, {
      tenant: create(TenantSchema, { id: 'ten_1' }),
      bindings: bindings.map((b) => create(RoleBindingSchema, b)),
    });

  it('requires a binding covering the whole tenant', () => {
    const scope = (tenant: ReturnType<typeof tenantWith>) => ({ isPlatformAdmin: false, tenant });
    expect(checkTenantPermission(scope(tenantWith({ role: 'owner' })), 'user:read')).toBe(true);
    expect(
      checkTenantPermission(scope(tenantWith({ role: 'owner', namespaceId: 'ns_1' })), 'user:read'),
    ).toBe(false);
    expect(checkTenantPermission(scope(tenantWith({ role: 'owner', siteIds: ['sit_a'] })), 'user:read')).toBe(
      false,
    );
    expect(checkTenantPermission(scope(tenantWith({ role: 'admin' })), 'user:read')).toBe(false);
    expect(checkTenantPermission(scope(tenantWith({ role: 'owner' })), 'tenant:manage')).toBe(false);
  });

  it('grants everything to platform admins', () => {
    expect(checkTenantPermission({ isPlatformAdmin: true, tenant: undefined }, 'user:write')).toBe(true);
    expect(checkTenantPermission({ isPlatformAdmin: false, tenant: undefined }, 'user:read')).toBe(false);
  });
});

describe('resolveSelection', () => {
  const tenants = [
    create(TenantAccessSchema, {
      tenant: create(TenantSchema, { id: 'ten_1', name: 'one' }),
      namespaces: [
        create(NamespaceAccessSchema, { namespace: create(NamespaceSchema, { id: 'ns_a', name: 'alpha' }) }),
        create(NamespaceAccessSchema, {
          namespace: create(NamespaceSchema, { id: 'ns_d', name: 'default' }),
        }),
      ],
    }),
    create(TenantAccessSchema, {
      tenant: create(TenantSchema, { id: 'ten_2', name: 'two' }),
      namespaces: [
        create(NamespaceAccessSchema, { namespace: create(NamespaceSchema, { id: 'ns_x', name: 'x' }) }),
      ],
    }),
  ];

  it('keeps a valid stored selection', () => {
    const s = resolveSelection(tenants, 'ten_2', 'x');
    expect(s.tenant?.tenant?.id).toBe('ten_2');
    expect(s.namespace?.namespace?.name).toBe('x');
  });

  it('falls back to the first tenant and the default namespace', () => {
    const s = resolveSelection(tenants, 'ten_gone', 'missing');
    expect(s.tenant?.tenant?.id).toBe('ten_1');
    expect(s.namespace?.namespace?.name).toBe('default');
  });

  it('falls back to the first namespace when there is no default', () => {
    expect(resolveSelection(tenants, 'ten_2', undefined).namespace?.namespace?.name).toBe('x');
    expect(resolveSelection([], 'ten_1', 'x')).toEqual({ tenant: undefined, namespace: undefined });
  });
});
