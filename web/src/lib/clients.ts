import { createClient, type Client } from '@connectrpc/connect';

import { AccessAdminService } from '@/gen/spinneret/v1/access_admin_pb';
import { AuthService } from '@/gen/spinneret/v1/auth_pb';
import { BreakerAdminService } from '@/gen/spinneret/v1/breaker_admin_pb';
import { ConfigAdminService } from '@/gen/spinneret/v1/config_admin_pb';
import { ConfigService } from '@/gen/spinneret/v1/config_pb';
import { DashboardService } from '@/gen/spinneret/v1/dashboard_pb';
import { IdentityAdminService } from '@/gen/spinneret/v1/identity_admin_pb';
import { LeaseService } from '@/gen/spinneret/v1/lease_pb';
import { NotificationAdminService } from '@/gen/spinneret/v1/notification_admin_pb';
import { PolicyAdminService } from '@/gen/spinneret/v1/policy_admin_pb';
import { ProxyAdminService } from '@/gen/spinneret/v1/proxy_admin_pb';
import { ReportService } from '@/gen/spinneret/v1/report_pb';
import { SecretAdminService } from '@/gen/spinneret/v1/secret_admin_pb';
import { SecretService } from '@/gen/spinneret/v1/secret_pb';
import { SiteAdminService } from '@/gen/spinneret/v1/site_admin_pb';
import { TenantAdminService } from '@/gen/spinneret/v1/tenant_admin_pb';

import { transport } from './transport';

// Console (session-authenticated) services.
export const authClient: Client<typeof AuthService> = createClient(AuthService, transport);
export const tenantClient: Client<typeof TenantAdminService> = createClient(TenantAdminService, transport);
export const accessClient: Client<typeof AccessAdminService> = createClient(AccessAdminService, transport);
export const siteClient: Client<typeof SiteAdminService> = createClient(SiteAdminService, transport);
export const identityClient: Client<typeof IdentityAdminService> = createClient(
  IdentityAdminService,
  transport,
);
export const proxyClient: Client<typeof ProxyAdminService> = createClient(ProxyAdminService, transport);
export const policyClient: Client<typeof PolicyAdminService> = createClient(PolicyAdminService, transport);
export const breakerClient: Client<typeof BreakerAdminService> = createClient(BreakerAdminService, transport);
export const configAdminClient: Client<typeof ConfigAdminService> = createClient(
  ConfigAdminService,
  transport,
);
export const secretAdminClient: Client<typeof SecretAdminService> = createClient(
  SecretAdminService,
  transport,
);
export const notificationClient: Client<typeof NotificationAdminService> = createClient(
  NotificationAdminService,
  transport,
);
export const dashboardClient: Client<typeof DashboardService> = createClient(DashboardService, transport);

// Node-facing services (token-authenticated); exposed for completeness and tooling pages.
export const leaseClient: Client<typeof LeaseService> = createClient(LeaseService, transport);
export const reportClient: Client<typeof ReportService> = createClient(ReportService, transport);
export const configClient: Client<typeof ConfigService> = createClient(ConfigService, transport);
export const secretClient: Client<typeof SecretService> = createClient(SecretService, transport);
