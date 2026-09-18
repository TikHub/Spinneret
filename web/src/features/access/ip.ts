/**
 * IP address and CIDR prefix validation matching the CreateTokenRequest
 * ip_allowlist rule (`isIp() || isIpPrefix()`): dotted-quad IPv4 without
 * leading zeros, RFC 4291 IPv6 text (with "::" and an embedded IPv4 tail, no
 * zone), and "<ip>/<bits>" prefixes.
 */

const IPV4_PATTERN = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const HEX_GROUP = /^[0-9a-fA-F]{1,4}$/;
const PREFIX_BITS = /^(0|[1-9]\d{0,2})$/;

/** Reports whether the string is an IPv4 address. */
export function isIPv4(value: string): boolean {
  return IPV4_PATTERN.test(value);
}

/** Reports whether the string is an IPv6 address. */
export function isIPv6(value: string): boolean {
  if (value.length < 2 || value.length > 45) return false;
  const lastColon = value.lastIndexOf(':');
  if (lastColon === -1) return false;
  let text = value;
  const tail = value.slice(lastColon + 1);
  if (tail.includes('.')) {
    // An embedded IPv4 address counts as two 16-bit groups.
    if (!isIPv4(tail)) return false;
    text = `${value.slice(0, lastColon + 1)}0:0`;
  }
  const compressed = text.indexOf('::');
  if (compressed !== text.lastIndexOf('::')) return false;
  if (compressed === -1) {
    const groups = text.split(':');
    return groups.length === 8 && groups.every((g) => HEX_GROUP.test(g));
  }
  const head = text.slice(0, compressed);
  const rest = text.slice(compressed + 2);
  const groups = [...(head === '' ? [] : head.split(':')), ...(rest === '' ? [] : rest.split(':'))];
  return groups.length <= 7 && groups.every((g) => HEX_GROUP.test(g));
}

/** Reports whether the string is an IPv4 or IPv6 address. */
export function isIpAddress(value: string): boolean {
  return isIPv4(value) || isIPv6(value);
}

/** Reports whether the string is a CIDR prefix such as "10.0.0.0/8" or "2001:db8::/32". */
export function isCidr(value: string): boolean {
  const slash = value.indexOf('/');
  if (slash === -1 || slash !== value.lastIndexOf('/')) return false;
  const address = value.slice(0, slash);
  const bits = value.slice(slash + 1);
  if (!PREFIX_BITS.test(bits)) return false;
  const size = Number(bits);
  if (isIPv4(address)) return size <= 32;
  if (isIPv6(address)) return size <= 128;
  return false;
}

/** Reports whether the string is an IP address or a CIDR prefix. */
export function isIpOrCidr(value: string): boolean {
  const trimmed = value.trim();
  return trimmed !== '' && trimmed === value && (isIpAddress(value) || isCidr(value));
}
