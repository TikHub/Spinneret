import { describe, expect, it } from 'vitest';

import { isCidr, isIPv4, isIPv6, isIpOrCidr } from './ip';

describe('isIPv4', () => {
  it.each(['0.0.0.0', '203.0.113.7', '255.255.255.255', '10.0.0.1'])('accepts %s', (ip) => {
    expect(isIPv4(ip)).toBe(true);
  });

  it.each(['256.0.0.1', '1.2.3', '1.2.3.4.5', '01.2.3.4', '1.2.3.-4', 'a.b.c.d', ' 1.2.3.4', ''])(
    'rejects %s',
    (ip) => {
      expect(isIPv4(ip)).toBe(false);
    },
  );
});

describe('isIPv6', () => {
  it.each([
    '::',
    '::1',
    '2001:db8::',
    '2001:db8:0:0:0:0:2:1',
    'fe80::1ff:fe23:4567:890a',
    '::ffff:192.0.2.128',
    '1:2:3:4:5:6:7::',
    '1:2:3:4:5:6:1.2.3.4',
    'ABCD:EF01::',
  ])('accepts %s', (ip) => {
    expect(isIPv6(ip)).toBe(true);
  });

  it.each([
    ':::',
    '1::2::3',
    '1:2:3:4:5:6:7:8:9',
    '1:2:3:4:5:6:7',
    '12345::',
    'fe80::1%eth0',
    '1:2:3:4:5:6:7:8::',
    'fe80:1.2.3.4',
    '::ffff:999.1.1.1',
    '1:2:3:4:5:6:7:',
    'g::1',
    '203.0.113.7',
  ])('rejects %s', (ip) => {
    expect(isIPv6(ip)).toBe(false);
  });
});

describe('isCidr', () => {
  it.each(['10.0.0.0/8', '203.0.113.7/32', '0.0.0.0/0', '2001:db8::/32', '::/0', '::1/128'])(
    'accepts %s',
    (cidr) => {
      expect(isCidr(cidr)).toBe(true);
    },
  );

  it.each(['10.0.0.0/33', '10.0.0.0/', '10.0.0.0/08', '2001:db8::/129', '10.0.0.0/8/1', 'x/8', '/8'])(
    'rejects %s',
    (cidr) => {
      expect(isCidr(cidr)).toBe(false);
    },
  );
});

describe('isIpOrCidr', () => {
  it('accepts addresses and prefixes but not hostnames or padded values', () => {
    expect(isIpOrCidr('203.0.113.7')).toBe(true);
    expect(isIpOrCidr('10.0.0.0/8')).toBe(true);
    expect(isIpOrCidr('2001:db8::/32')).toBe(true);
    expect(isIpOrCidr('example.com')).toBe(false);
    expect(isIpOrCidr(' 10.0.0.1')).toBe(false);
    expect(isIpOrCidr('')).toBe(false);
  });
});
