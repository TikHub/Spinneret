import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { CONSOLE_EVENT_TYPES } from '@/lib/queryKeys';
import { useEventSource, type SseStatus } from '@/lib/sse';

import { invalidateForEvent, parseConsoleEvent, toastForEvent } from './consoleEvents';

export const EVENTS_STREAM_PATH = '/api/v1/events/stream';

/** Builds the SSE URL for a tenant and namespace. */
export function eventsStreamUrl(tenantId: string, namespace: string): string {
  const params = new URLSearchParams({ tenant: tenantId, namespace });
  return `${EVENTS_STREAM_PATH}?${params.toString()}`;
}

/**
 * Subscribes to console events of the active namespace: toasts for breaker
 * transitions and alerts, and React Query invalidation by key prefix.
 */
export function useConsoleEvents(): SseStatus {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { status, tenantId, namespaceName } = useAuth();
  const url =
    status === 'authenticated' && tenantId && namespaceName ? eventsStreamUrl(tenantId, namespaceName) : null;

  return useEventSource(
    url,
    (message) => {
      const event = parseConsoleEvent(message);
      if (!event) return;
      invalidateForEvent(queryClient, event);
      const spec = toastForEvent(event, t);
      if (spec) toast[spec.level](spec.title, { description: spec.description });
    },
    { eventTypes: CONSOLE_EVENT_TYPES },
  );
}
