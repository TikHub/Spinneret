import { describe, expect, it } from 'vitest';

import {
  configForUpdate,
  configToForm,
  EMPTY_CHANNEL_CONFIG,
  formToConfig,
  isMaskedValue,
  sameConfig,
  validateChannelConfig,
} from './channelConfig';

describe('configToForm / formToConfig', () => {
  it('round-trips webhook settings with headers', () => {
    const config = {
      url: 'https://hooks.example.com/alert',
      secret: '••••abcd',
      headers: { 'X-Team': 'ops', Authorization: '••••1234' },
    };
    const form = configToForm(config);
    expect(form.url).toBe('https://hooks.example.com/alert');
    expect(form.headers).toEqual([
      { key: 'Authorization', value: '••••1234' },
      { key: 'X-Team', value: 'ops' },
    ]);
    expect(formToConfig('webhook', form)).toEqual(config);
  });

  it('keeps only the fields of the kind and drops empty optional fields', () => {
    const form = {
      ...EMPTY_CHANNEL_CONFIG,
      url: 'https://x',
      webhookUrl: ' https://open.feishu.cn/hook ',
      secret: '',
    };
    expect(formToConfig('feishu', form)).toEqual({ webhook_url: 'https://open.feishu.cn/hook' });
    expect(formToConfig('wecom', { ...form, secret: 'ignored' })).toEqual({
      webhook_url: 'https://open.feishu.cn/hook',
    });
  });

  it('converts telegram settings and numeric chat ids to strings', () => {
    const form = configToForm({ bot_token: '••••wxyz', chat_id: -100123, api_base: 'https://tg.example' });
    expect(form).toMatchObject({ botToken: '••••wxyz', chatId: '-100123', apiBase: 'https://tg.example' });
    expect(formToConfig('telegram', form)).toEqual({
      bot_token: '••••wxyz',
      chat_id: '-100123',
      api_base: 'https://tg.example',
    });
  });

  it('ignores header rows without a name', () => {
    const form = { ...EMPTY_CHANNEL_CONFIG, url: 'https://x.io', headers: [{ key: ' ', value: 'v' }] };
    expect(formToConfig('webhook', form)).toEqual({ url: 'https://x.io' });
  });

  it('compares settings regardless of key order', () => {
    expect(sameConfig({ a: '1', b: { c: '2', d: '3' } }, { b: { d: '3', c: '2' }, a: '1' })).toBe(true);
    expect(sameConfig({ a: '1' }, { a: '2' })).toBe(false);
  });
});

describe('configForUpdate (masked secret handling)', () => {
  const stored = { webhook_url: 'https://open.feishu.cn/••••wxyz', secret: '••••abcd' };

  it('keeps the stored settings when nothing changed', () => {
    expect(configForUpdate('feishu', configToForm(stored), stored)).toBeUndefined();
  });

  it('sends untouched masked values verbatim when another field changed', () => {
    const form = { ...configToForm(stored), secret: 'new-signing-secret' };
    expect(configForUpdate('feishu', form, stored)).toEqual({
      webhook_url: 'https://open.feishu.cn/••••wxyz',
      secret: 'new-signing-secret',
    });
  });

  it('drops a cleared secret so the server removes it', () => {
    const form = { ...configToForm(stored), secret: '' };
    expect(configForUpdate('feishu', form, stored)).toEqual({
      webhook_url: 'https://open.feishu.cn/••••wxyz',
    });
  });

  it('detects masked values', () => {
    expect(isMaskedValue('••••abcd')).toBe(true);
    expect(isMaskedValue('https://h/••••abcd')).toBe(true);
    expect(isMaskedValue('plain')).toBe(false);
  });
});

describe('validateChannelConfig', () => {
  it('requires kind-specific fields', () => {
    expect(validateChannelConfig('webhook', EMPTY_CHANNEL_CONFIG)).toEqual({ url: 'required' });
    expect(validateChannelConfig('telegram', EMPTY_CHANNEL_CONFIG)).toEqual({
      botToken: 'required',
      chatId: 'required',
    });
    expect(validateChannelConfig('wecom', EMPTY_CHANNEL_CONFIG)).toEqual({ webhookUrl: 'required' });
  });

  it('validates URLs and bot tokens', () => {
    const form = { ...EMPTY_CHANNEL_CONFIG, botToken: 'nope', chatId: '1', apiBase: 'ftp://x' };
    expect(validateChannelConfig('telegram', form)).toEqual({ botToken: 'botToken', apiBase: 'url' });
    const ok = { ...form, botToken: '123:ABC_def-1', apiBase: 'https://api.telegram.org' };
    expect(validateChannelConfig('telegram', ok)).toEqual({});
    expect(validateChannelConfig('dingtalk', { ...EMPTY_CHANNEL_CONFIG, webhookUrl: 'not a url' })).toEqual({
      webhookUrl: 'url',
    });
  });

  it('accepts untouched masked values and rejects edited ones', () => {
    const initial = configToForm({ webhook_url: 'https://oapi.dingtalk.com/••••wxyz', secret: '••••abcd' });
    expect(validateChannelConfig('dingtalk', initial, initial)).toEqual({});
    const edited = { ...initial, secret: '••••abcdX', webhookUrl: 'https://oapi.dingtalk.com/••••wxyz?x=1' };
    expect(validateChannelConfig('dingtalk', edited, initial)).toEqual({
      webhookUrl: 'masked',
      secret: 'masked',
    });
    // A masked value on create cannot be restored by the server.
    expect(validateChannelConfig('dingtalk', initial)).toEqual({ webhookUrl: 'masked', secret: 'masked' });
  });

  it('validates headers', () => {
    const base = { ...EMPTY_CHANNEL_CONFIG, url: 'https://h.io' };
    const withHeaders = (headers: { key: string; value: string }[]) =>
      validateChannelConfig('webhook', { ...base, headers }).headers;
    expect(withHeaders([{ key: 'X-A', value: '1' }])).toBeUndefined();
    expect(withHeaders([{ key: 'bad header', value: '1' }])).toBe('headerName');
    expect(withHeaders([{ key: 'Content-Type', value: 'x' }])).toBe('headerReserved');
    expect(
      withHeaders([
        { key: 'X-A', value: '1' },
        { key: 'x-a', value: '2' },
      ]),
    ).toBe('headerDuplicate');
    expect(withHeaders(Array.from({ length: 21 }, (_, i) => ({ key: `X-${i}`, value: 'v' })))).toBe(
      'headerLimit',
    );
    expect(withHeaders([{ key: 'Authorization', value: '••••1234' }])).toBe('headerMasked');
  });

  it('requires masked credential headers again when the webhook URL changes', () => {
    const initial = configToForm({
      url: 'https://hooks.example.com/••••alrt',
      headers: { Authorization: '••••1234', 'X-Team': 'ops' },
    });
    expect(validateChannelConfig('webhook', initial, initial)).toEqual({});
    const moved = { ...initial, url: 'https://evil.example.com/collect' };
    expect(validateChannelConfig('webhook', moved, initial)).toEqual({ headers: 'headerMaskedDestination' });
    const reentered = {
      ...moved,
      headers: [
        { key: 'Authorization', value: 'Bearer abc' },
        { key: 'X-Team', value: 'ops' },
      ],
    };
    expect(validateChannelConfig('webhook', reentered, initial)).toEqual({});
  });

  it('requires the bot token again when the Telegram API base changes', () => {
    const initial = configToForm({ bot_token: '••••wxyz', chat_id: '42' });
    expect(validateChannelConfig('telegram', initial, initial)).toEqual({});
    // The default base written out (with a trailing slash) is the same destination.
    expect(
      validateChannelConfig('telegram', { ...initial, apiBase: 'https://api.telegram.org/' }, initial),
    ).toEqual({});
    const moved = { ...initial, apiBase: 'https://tg-proxy.example.com' };
    expect(validateChannelConfig('telegram', moved, initial)).toEqual({ botToken: 'maskedDestination' });
    expect(validateChannelConfig('telegram', { ...moved, botToken: '123:ABC' }, initial)).toEqual({});
  });

  it('treats an api_base with masked credentials as replace-only', () => {
    const initial = configToForm({ bot_token: '••••wxyz', chat_id: '42', api_base: 'https://h.io/••••pass' });
    expect(validateChannelConfig('telegram', initial, initial)).toEqual({});
    const edited = { ...initial, apiBase: 'https://h.io/••••passX' };
    expect(validateChannelConfig('telegram', edited, initial)).toMatchObject({ apiBase: 'masked' });
  });
});
