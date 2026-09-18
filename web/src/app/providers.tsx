import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { type ReactNode } from 'react';
import { Toaster } from 'sonner';

import { AuthProvider } from '@/app/auth/AuthContext';
import { ThemeProvider, useTheme } from '@/app/theme/ThemeProvider';
import { UnsavedChangesProvider } from '@/app/unsaved/UnsavedChangesProvider';
import { TooltipProvider } from '@/components/ui/tooltip';

function ThemedToaster() {
  const { resolvedTheme } = useTheme();
  return <Toaster theme={resolvedTheme} position="bottom-right" richColors closeButton duration={6000} />;
}

export interface AppProvidersProps {
  queryClient: QueryClient;
  children: ReactNode;
}

/**
 * Application-wide providers: React Query, theme, tooltips, auth, the unsaved-changes
 * registry and toasts (i18n is initialized in src/i18n).
 */
export function AppProviders({ queryClient, children }: AppProvidersProps) {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <TooltipProvider delayDuration={300}>
          <AuthProvider>
            <UnsavedChangesProvider>{children}</UnsavedChangesProvider>
            <ThemedToaster />
          </AuthProvider>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
