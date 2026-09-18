import { type MessageInitShape } from '@bufbuild/protobuf';

import {
  type Channel,
  type CreateChannelRequestSchema,
  type UpdateChannelRequestSchema,
} from '@/gen/spinneret/v1/notification_admin_pb';

import {
  configForUpdate,
  configToForm,
  DEFAULT_MIN_SEVERITY,
  EMPTY_CHANNEL_CONFIG,
  formToConfig,
  isChannelKind,
  MAX_CHANNEL_NAME_LENGTH,
  validateChannelConfig,
  type AlertKind,
  type ChannelConfigErrors,
  type ChannelConfigForm,
  type ChannelKind,
  type Severity,
} from './channelConfig';

/** Channel scope: tenant-wide or bound to a namespace. */
export type ChannelScope = 'tenant' | 'namespace';

export interface ChannelFormValues {
  name: string;
  scope: ChannelScope;
  /** Namespace name for namespace channels. */
  namespace: string;
  kind: ChannelKind;
  config: ChannelConfigForm;
  eventTypes: string[];
  sites: string[];
  minSeverity: Severity;
  enabled: boolean;
}

/** Event types preselected for new channels. */
export const DEFAULT_EVENT_TYPES: readonly AlertKind[] = [
  'breaker_opened',
  'breaker_reopened',
  'identity_low_watermark',
  'proxy_low_watermark',
  'ban_spike',
  'secret_expiring',
];

export function newChannelForm(namespace: string, scope: ChannelScope): ChannelFormValues {
  return {
    name: '',
    scope,
    namespace,
    kind: 'webhook',
    config: EMPTY_CHANNEL_CONFIG,
    eventTypes: [...DEFAULT_EVENT_TYPES],
    sites: [],
    minSeverity: DEFAULT_MIN_SEVERITY,
    enabled: true,
  };
}

function toSeverity(value: string): Severity {
  return value === 'info' || value === 'warning' || value === 'critical' ? value : DEFAULT_MIN_SEVERITY;
}

export function formFromChannel(channel: Channel): ChannelFormValues {
  return {
    name: channel.name,
    scope: channel.namespace === '' ? 'tenant' : 'namespace',
    namespace: channel.namespace,
    kind: isChannelKind(channel.kind) ? channel.kind : 'webhook',
    config: configToForm(channel.config),
    eventTypes: [...channel.eventTypes],
    sites: [...channel.sites],
    minSeverity: toSeverity(channel.minSeverity),
    enabled: channel.enabled,
  };
}

export interface ChannelFormErrors {
  name?: 'required' | 'tooLong';
  eventTypes?: 'required';
  sites?: 'tenantScope';
  config: ChannelConfigErrors;
}

/** Validates the form; `initial` is the channel being edited (masked values stay valid). */
export function validateChannelForm(values: ChannelFormValues, initial?: Channel): ChannelFormErrors {
  const errors: ChannelFormErrors = {
    config: validateChannelConfig(
      values.kind,
      values.config,
      initial ? configToForm(initial.config) : undefined,
    ),
  };
  const name = values.name.trim();
  if (name === '') errors.name = 'required';
  else if (name.length > MAX_CHANNEL_NAME_LENGTH) errors.name = 'tooLong';
  if (values.eventTypes.length === 0) errors.eventTypes = 'required';
  if (values.scope === 'tenant' && values.sites.length > 0) errors.sites = 'tenantScope';
  return errors;
}

export function hasChannelErrors(errors: ChannelFormErrors): boolean {
  return (
    errors.name !== undefined ||
    errors.eventTypes !== undefined ||
    errors.sites !== undefined ||
    Object.keys(errors.config).length > 0
  );
}

function uniq(values: readonly string[]): string[] {
  return [...new Set(values)];
}

export function createChannelRequest(
  values: ChannelFormValues,
): MessageInitShape<typeof CreateChannelRequestSchema> {
  const namespaced = values.scope === 'namespace';
  return {
    namespace: namespaced ? values.namespace : '',
    name: values.name.trim(),
    kind: values.kind,
    config: formToConfig(values.kind, values.config),
    eventTypes: uniq(values.eventTypes),
    sites: namespaced ? uniq(values.sites) : [],
    minSeverity: values.minSeverity,
    enabled: values.enabled,
  };
}

/**
 * UpdateChannel replaces the mutable fields; `config` is omitted (server keeps
 * the stored settings) unless the kind-specific settings changed.
 */
export function updateChannelRequest(
  channel: Channel,
  values: ChannelFormValues,
): MessageInitShape<typeof UpdateChannelRequestSchema> {
  const kind = isChannelKind(channel.kind) ? channel.kind : values.kind;
  const config = configForUpdate(kind, values.config, channel.config);
  return {
    id: channel.id,
    name: values.name.trim(),
    ...(config ? { config } : {}),
    eventTypes: uniq(values.eventTypes),
    sites: channel.namespace === '' ? [] : uniq(values.sites),
    minSeverity: values.minSeverity,
    enabled: values.enabled,
  };
}

/** UpdateChannel request that only flips `enabled` (every other field unchanged, settings kept). */
export function toggleChannelRequest(
  channel: Channel,
  enabled: boolean,
): MessageInitShape<typeof UpdateChannelRequestSchema> {
  return {
    id: channel.id,
    name: channel.name,
    eventTypes: [...channel.eventTypes],
    sites: [...channel.sites],
    minSeverity: channel.minSeverity,
    enabled,
  };
}
