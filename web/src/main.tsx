import { RouterProvider } from '@tanstack/react-router';
import { StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';

import './index.css';
import '@/i18n';
import { installAuthBridge } from '@/app/auth/session';
import { GlobalErrorBoundary } from '@/app/errors/GlobalErrorBoundary';
import { AppProviders } from '@/app/providers';
import { createQueryClient } from '@/app/queryClient';
import { FullPageLoader } from '@/components/FullPageLoader';
import { router } from '@/router';

const queryClient = createQueryClient();
installAuthBridge(queryClient);

const container = document.getElementById('root');
if (!container) {
  throw new Error('Missing #root element');
}

createRoot(container).render(
  <StrictMode>
    <GlobalErrorBoundary>
      <AppProviders queryClient={queryClient}>
        <Suspense fallback={<FullPageLoader />}>
          <RouterProvider router={router} />
        </Suspense>
      </AppProviders>
    </GlobalErrorBoundary>
  </StrictMode>,
);
