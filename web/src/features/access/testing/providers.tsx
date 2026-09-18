import { create } from '@bufbuild/protobuf';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import { Suspense, type ReactElement } from 'react';

import '@/i18n';
import { AuthProvider } from '@/app/auth/AuthContext';
import { TooltipProvider } from '@/components/ui/tooltip';
import {
  GetMeResponseSchema,
  NamespaceAccessSchema,
  NamespaceSchema,
  RoleBindingSchema,
  TenantAccessSchema,
  TenantSchema,
  UserSchema,
  type GetMeResponse,
} from '@/gen/spinneret/v1/auth_pb';

export interface MeOptions {
  isPlatformAdmin?: boolean;
  role?: string;
  /** Namespace-wide permissions of the "prod" namespace. */
  permissions?: string[];
}

/** GetMe response with one tenant ("acme") and one namespace ("prod"). */
export function meResponse({
  isPlatformAdmin = false,
  role = 'owner',
  permissions = [],
}: MeOptions = {}): GetMeResponse {
  return create(GetMeResponseSchema, {
    user: create(UserSchema, { id: 'usr_me', username: 'me', isPlatformAdmin }),
    tenants: [
      create(TenantAccessSchema, {
        tenant: create(TenantSchema, { id: 'ten_1', name: 'acme', displayName: 'Acme' }),
        bindings: [create(RoleBindingSchema, { id: 'rb_me', userId: 'usr_me', tenantId: 'ten_1', role })],
        namespaces: [
          create(NamespaceAccessSchema, {
            namespace: create(NamespaceSchema, { id: 'ns_1', tenantId: 'ten_1', name: 'prod' }),
            permissions,
          }),
        ],
      }),
    ],
  });
}

/** Renders a page with React Query, tooltips, auth (mocked clients) and i18n. */
export function renderWithProviders(ui: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <AuthProvider>
          <Suspense fallback={<div>loading</div>}>{ui}</Suspense>
        </AuthProvider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
  return { queryClient, ...result };
}
