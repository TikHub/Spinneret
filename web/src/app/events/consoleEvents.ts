import { type QueryClient } from '@tanstack/react-query';

import { type Translate } from '@/lib/errors';
import {
  CONSOLE_EVENT_TYPES,
  EVENT_INVALIDATIONS,
  type ConsoleEventType,
  type QueryDomain,
} from '@/lib/queryKeys';
import { type SseMessage } from '@/lib/sse';

/**
 * Envelope of console events (internal/events.Event). The stream sends one SSE
 * message per event: `event: <type>` and `data: {"type","tenant_id"?,
 * "namespace_id"?,"site_id"?,"at","data"?:{...}}`.
 */
export interface ConsoleEvent {
  type: string;
  tenantId?: string;
  namespaceId?: string;
  siteId?: string;
  /** RFC 3339 time the event was produced. */
  at?: string;
  /** Type-specific payload (snake_case keys as sent by the server); see the *EventData types. */
  data: Record<string, unknown>;
}

/**
 * Payload of breaker.transition (breaker.TransitionData). Site pauses and
 * resumptions use the same payload with trigger "site_switch", from/to
 * "running"/"paused" and no endpoint group.
 */
export interface BreakerTransitionEventData {
  namespace: string;
  /** Site name. */
  site: string;
  site_id: string;
  client: string;
  /** Endpoint group name. */
  endpoint_group: string;
  endpoint_group_id: string;
  from: string;
  to: string;
  /** auto | manual | probe | site_switch */
  trigger: string;
  reason: string;
  open_until: string | null;
  consecutive_opens: number;
  manual: boolean;
  actor: string;
  metrics: Record<string, number>;
}

/** Payload of alert events (notify alertEventData). */
export interface AlertEventData {
  id: string;
  kind: string;
  /** info | warning | critical */
  severity: string;
  title: string;
  message: string;
  namespace_id?: string;
  site_id?: string;
  created_at: string;
}

/** Payload of identity.state and proxy.state events (action.StateEventData). */
export interface StateEventData {
  subject_kind: string;
  subject_id: string;
  subject_key?: number;
  site_id: string;
  from: string;
  to: string;
  action: string;
  scope?: string;
  until: string | null;
  permanent?: boolean;
  reason: string;
  actor?: string;
}

/** Payload of config.published events on the namespace channel (configcenter.PublishedEventData). */
export interface ConfigPublishedEventData {
  item_id: string;
  group: string;
  key: string;
  version: number;
  source_version?: number;
  actor: string;
}

/** Payload of policy.published events (policysvc.PublishedEvent). */
export interface PolicyPublishedEventData {
  policy_id: string;
  name: string;
  kind: string;
  version: number;
  action: string;
  binding_id?: string;
  site_id?: string;
  actor: string;
}

export interface ToastSpec {
  level: 'success' | 'warning' | 'error' | 'info';
  title: string;
  description?: string;
}

/** SSE event name of unnamed messages. */
const UNNAMED_EVENT = 'message';

/**
 * Alert kinds that never toast: breaker alerts duplicate the breaker.transition
 * toast, identity expirations are high-volume informational alerts.
 */
const QUIET_ALERT_KINDS: ReadonlySet<string> = new Set([
  'breaker_opened',
  'breaker_reopened',
  'breaker_closed',
  'identity_expired',
]);

/** Delay used to coalesce invalidations of bursts of events into one refetch per domain. */
export const INVALIDATION_COALESCE_MS = 500;

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

/** First non-empty string field among the keys. */
export function pickString(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = obj[key];
    if (typeof value === 'string' && value !== '') return value;
  }
  return '';
}

export function isConsoleEventType(type: string): type is ConsoleEventType {
  return (CONSOLE_EVENT_TYPES as readonly string[]).includes(type);
}

/**
 * Normalizes an SSE message into a console event. The named SSE event wins
 * over the envelope `type`; unnamed messages carry the type in the envelope.
 * A missing or malformed payload becomes an empty object.
 */
export function parseConsoleEvent(message: SseMessage): ConsoleEvent | undefined {
  const body = asRecord(message.data);
  const envelopeType = body ? pickString(body, 'type') : '';
  const type = message.type && message.type !== UNNAMED_EVENT ? message.type : envelopeType;
  if (!type) return undefined;
  if (!body) return { type, data: {} };
  const optional = (key: string) => pickString(body, key) || undefined;
  return {
    type,
    tenantId: optional('tenant_id'),
    namespaceId: optional('namespace_id'),
    siteId: optional('site_id'),
    at: optional('at'),
    data: asRecord(body.data) ?? {},
  };
}

/** Breaker trigger of site pause/resume transitions (breaker.TriggerSiteSwitch). */
const SITE_SWITCH_TRIGGER = 'site_switch';
/** Site state after a pause (breaker.SitePaused). */
const SITE_PAUSED = 'paused';

function siteSwitchToast(event: ConsoleEvent, t: Translate): ToastSpec {
  const d = event.data;
  const site = pickString(d, 'site', 'site_id') || event.siteId || '—';
  const paused = pickString(d, 'to') === SITE_PAUSED;
  return {
    level: paused ? 'warning' : 'success',
    title: paused ? t('events.sitePaused', { site }) : t('events.siteResumed', { site }),
    description: pickString(d, 'reason') || undefined,
  };
}

function breakerToast(event: ConsoleEvent, t: Translate): ToastSpec {
  const d = event.data;
  if (pickString(d, 'trigger') === SITE_SWITCH_TRIGGER) return siteSwitchToast(event, t);
  const to = pickString(d, 'to');
  const from = pickString(d, 'from');
  const stateLabel = (s: string) => (s ? t(`states.${s}`, { defaultValue: s }) : '?');
  const group = pickString(d, 'endpoint_group');
  const client = pickString(d, 'client');
  const description = t('events.breakerDescription', {
    site: pickString(d, 'site', 'site_id') || event.siteId || '—',
    group: group ? (client ? `${client}/${group}` : group) : pickString(d, 'endpoint_group_id') || '—',
    from: stateLabel(from),
    to: stateLabel(to),
  });
  const reason = pickString(d, 'reason');
  return {
    level: to === 'open' ? 'error' : to === 'half_open' ? 'warning' : 'success',
    title: t('events.breakerTitle', { state: stateLabel(to) }),
    description: reason ? `${description} (${reason})` : description,
  };
}

function alertToast(event: ConsoleEvent, t: Translate): ToastSpec | undefined {
  const d = event.data;
  if (QUIET_ALERT_KINDS.has(pickString(d, 'kind'))) return undefined;
  const severity = pickString(d, 'severity');
  return {
    level: severity === 'critical' ? 'error' : severity === 'info' ? 'info' : 'warning',
    title: pickString(d, 'title') || t('events.alertTitle'),
    description: pickString(d, 'message') || t('events.alertFallback'),
  };
}

/** Toast for breaker transitions and alerts; undefined for other events. */
export function toastForEvent(event: ConsoleEvent, t: Translate): ToastSpec | undefined {
  if (event.type === 'breaker.transition') return breakerToast(event, t);
  if (event.type === 'alert') return alertToast(event, t);
  return undefined;
}

/** Query domains an event invalidates (empty for unknown events). */
export function invalidationDomains(event: ConsoleEvent): readonly QueryDomain[] {
  return isConsoleEventType(event.type) ? EVENT_INVALIDATIONS[event.type] : [];
}

interface PendingInvalidation {
  domains: Set<QueryDomain>;
  timer: ReturnType<typeof setTimeout>;
}

const pendingByClient = new WeakMap<QueryClient, PendingInvalidation>();

/** Immediately runs the invalidations scheduled for a query client. */
export function flushInvalidations(queryClient: QueryClient): void {
  const pending = pendingByClient.get(queryClient);
  if (!pending) return;
  clearTimeout(pending.timer);
  pendingByClient.delete(queryClient);
  for (const domain of pending.domains) {
    void queryClient.invalidateQueries({ queryKey: [domain] });
  }
}

/**
 * Invalidates the query domains affected by an event. Invalidations are
 * coalesced for INVALIDATION_COALESCE_MS so that a burst of events (e.g. many
 * identity state changes) triggers one refetch per domain instead of
 * cancelling and restarting in-flight requests on every event.
 */
export function invalidateForEvent(queryClient: QueryClient, event: ConsoleEvent): void {
  const domains = invalidationDomains(event);
  if (domains.length === 0) return;
  let pending = pendingByClient.get(queryClient);
  if (!pending) {
    pending = {
      domains: new Set(),
      timer: setTimeout(() => flushInvalidations(queryClient), INVALIDATION_COALESCE_MS),
    };
    pendingByClient.set(queryClient, pending);
  }
  for (const domain of domains) pending.domains.add(domain);
}
