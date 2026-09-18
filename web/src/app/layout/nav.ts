import {
  ActivityIcon,
  BellIcon,
  Building2Icon,
  ClipboardListIcon,
  FingerprintIcon,
  GaugeIcon,
  GlobeIcon,
  Grid3x3Icon,
  KeyRoundIcon,
  KeySquareIcon,
  LayoutDashboardIcon,
  NetworkIcon,
  ScrollTextIcon,
  ShapesIcon,
  ShieldAlertIcon,
  SlidersHorizontalIcon,
  UserRoundIcon,
  UsersIcon,
  type LucideIcon,
} from 'lucide-react';

import { PERMISSIONS } from '@/app/auth/permissions';

export interface NavItem {
  /** Route path. */
  to:
    | '/'
    | '/identities'
    | '/identity-types'
    | '/accounts'
    | '/proxies'
    | '/sites'
    | '/policies'
    | '/heatmap'
    | '/breakers'
    | '/config'
    | '/secrets'
    | '/requests'
    | '/risk-events'
    | '/notifications'
    | '/access/tokens'
    | '/access/users'
    | '/access/audit'
    | '/admin/tenants';
  /** common namespace label key. */
  labelKey: string;
  icon: LucideIcon;
  /** Visible when any of these permissions is granted. */
  anyOf: readonly string[];
  /** Tenant-level page (users, role bindings): checked with canInTenant instead of can. */
  tenantLevel?: boolean;
}

export interface NavGroup {
  id: string;
  labelKey: string;
  items: readonly NavItem[];
}

/** Sidebar structure (spec section 5). */
export const NAV_GROUPS: readonly NavGroup[] = [
  {
    id: 'overview',
    labelKey: 'nav.groups.overview',
    items: [
      { to: '/', labelKey: 'nav.overview', icon: LayoutDashboardIcon, anyOf: [PERMISSIONS.dashboardRead] },
    ],
  },
  {
    id: 'scheduling',
    labelKey: 'nav.groups.scheduling',
    items: [
      {
        to: '/identities',
        labelKey: 'nav.identities',
        icon: FingerprintIcon,
        anyOf: [PERMISSIONS.identityRead],
      },
      {
        to: '/identity-types',
        labelKey: 'nav.identityTypes',
        icon: ShapesIcon,
        anyOf: [PERMISSIONS.identityRead],
      },
      { to: '/accounts', labelKey: 'nav.accounts', icon: UserRoundIcon, anyOf: [PERMISSIONS.identityRead] },
      { to: '/proxies', labelKey: 'nav.proxies', icon: NetworkIcon, anyOf: [PERMISSIONS.proxyRead] },
      { to: '/sites', labelKey: 'nav.sites', icon: GlobeIcon, anyOf: [PERMISSIONS.siteRead] },
      { to: '/policies', labelKey: 'nav.policies', icon: ScrollTextIcon, anyOf: [PERMISSIONS.policyRead] },
      { to: '/heatmap', labelKey: 'nav.heatmap', icon: Grid3x3Icon, anyOf: [PERMISSIONS.dashboardRead] },
      { to: '/breakers', labelKey: 'nav.breakers', icon: GaugeIcon, anyOf: [PERMISSIONS.breakerRead] },
    ],
  },
  {
    id: 'configuration',
    labelKey: 'nav.groups.configuration',
    items: [
      { to: '/config', labelKey: 'nav.config', icon: SlidersHorizontalIcon, anyOf: [PERMISSIONS.configRead] },
      { to: '/secrets', labelKey: 'nav.secrets', icon: KeyRoundIcon, anyOf: [PERMISSIONS.secretList] },
    ],
  },
  {
    id: 'observability',
    labelKey: 'nav.groups.observability',
    items: [
      { to: '/requests', labelKey: 'nav.requests', icon: ActivityIcon, anyOf: [PERMISSIONS.dashboardRead] },
      {
        to: '/risk-events',
        labelKey: 'nav.riskEvents',
        icon: ShieldAlertIcon,
        anyOf: [PERMISSIONS.dashboardRead],
      },
      {
        to: '/notifications',
        labelKey: 'nav.notifications',
        icon: BellIcon,
        anyOf: [PERMISSIONS.notifyRead],
      },
    ],
  },
  {
    id: 'access',
    labelKey: 'nav.groups.access',
    items: [
      { to: '/access/tokens', labelKey: 'nav.tokens', icon: KeySquareIcon, anyOf: [PERMISSIONS.tokenRead] },
      {
        to: '/access/users',
        labelKey: 'nav.users',
        icon: UsersIcon,
        anyOf: [PERMISSIONS.userRead],
        tenantLevel: true,
      },
      { to: '/access/audit', labelKey: 'nav.audit', icon: ClipboardListIcon, anyOf: [PERMISSIONS.auditRead] },
    ],
  },
  {
    id: 'platform',
    labelKey: 'nav.groups.platform',
    items: [
      {
        to: '/admin/tenants',
        labelKey: 'nav.tenants',
        icon: Building2Icon,
        anyOf: [PERMISSIONS.tenantManage, PERMISSIONS.namespaceWrite],
      },
    ],
  },
];

/** Permission checks used to decide which entries are visible. */
export interface NavPermissionChecks {
  canAny: (permissions: readonly string[]) => boolean;
  canInTenant: (permission: string) => boolean;
}

/** Reports whether the user may see a navigation entry. */
export function isNavItemVisible(item: NavItem, checks: NavPermissionChecks): boolean {
  return item.tenantLevel ? item.anyOf.some((p) => checks.canInTenant(p)) : checks.canAny(item.anyOf);
}

/** Groups with only the items the user may see (empty groups removed). */
export function visibleNavGroups(checks: NavPermissionChecks): NavGroup[] {
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => isNavItemVisible(item, checks)),
  })).filter((group) => group.items.length > 0);
}
