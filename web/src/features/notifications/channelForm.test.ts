import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import { ChannelSchema } from '@/gen/spinneret/v1/notification_admin_pb';

import {
  createChannelRequest,
  formFromChannel,
  hasChannelErrors,
  newChannelForm,
  toggleChannelRequest,
  updateChannelRequest,
  validateChannelForm,
} from './channelForm';

const feishu = create(ChannelSchema, {
  id: 'nch_1',
  namespace: 'prod',
  name: 'ops-feishu',
  kind: 'feishu',
  config: { webhook_url: 'https://open.feishu.cn/••••wxyz', secret: '••••abcd' },
  eventTypes: ['breaker_opened', 'ban_spike'],
  sites: ['shop'],
  minSeverity: 'critical',
  enabled: true,
});

describe('channel form', () => {
  it('builds create requests per scope', () => {
    const values = {
      ...newChannelForm('prod', 'namespace'),
      name: ' hooks ',
      config: { ...newChannelForm('prod', 'namespace').config, url: 'https://hooks.example.com' },
      sites: ['shop', 'shop'],
    };
    expect(createChannelRequest(values)).toMatchObject({
      namespace: 'prod',
      name: 'hooks',
      kind: 'webhook',
      config: { url: 'https://hooks.example.com' },
      sites: ['shop'],
      minSeverity: 'warning',
      enabled: true,
    });
    expect(createChannelRequest({ ...values, scope: 'tenant' })).toMatchObject({ namespace: '', sites: [] });
  });

  it('validates name, event types, tenant sites and settings', () => {
    const empty = { ...newChannelForm('prod', 'tenant'), eventTypes: [], sites: ['x'] };
    const errors = validateChannelForm(empty);
    expect(errors).toEqual({
      name: 'required',
      eventTypes: 'required',
      sites: 'tenantScope',
      config: { url: 'required' },
    });
    expect(hasChannelErrors(errors)).toBe(true);
    expect(validateChannelForm({ ...empty, name: 'n'.repeat(65) }).name).toBe('tooLong');
  });

  it('keeps masked settings valid when editing', () => {
    const errors = validateChannelForm(formFromChannel(feishu), feishu);
    expect(hasChannelErrors(errors)).toBe(false);
  });

  it('omits unchanged settings on update', () => {
    const request = updateChannelRequest(feishu, { ...formFromChannel(feishu), name: 'renamed' });
    expect(request).toEqual({
      id: 'nch_1',
      name: 'renamed',
      eventTypes: ['breaker_opened', 'ban_spike'],
      sites: ['shop'],
      minSeverity: 'critical',
      enabled: true,
    });
    expect('config' in request).toBe(false);
  });

  it('sends changed settings with untouched masked values', () => {
    const form = formFromChannel(feishu);
    const request = updateChannelRequest(feishu, { ...form, config: { ...form.config, secret: 'rotated' } });
    expect(request.config).toEqual({ webhook_url: 'https://open.feishu.cn/••••wxyz', secret: 'rotated' });
  });

  it('toggles enabled without touching settings', () => {
    expect(toggleChannelRequest(feishu, false)).toEqual({
      id: 'nch_1',
      name: 'ops-feishu',
      eventTypes: ['breaker_opened', 'ban_spike'],
      sites: ['shop'],
      minSeverity: 'critical',
      enabled: false,
    });
  });
});
