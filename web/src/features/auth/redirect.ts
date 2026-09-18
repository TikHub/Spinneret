/** Default destination after signing in. */
export const DEFAULT_REDIRECT = '/';

/** Placeholder origin used to resolve redirect targets without touching window.location. */
const PROBE_ORIGIN = 'http://spinneret.invalid';

/**
 * Accepts only same-origin relative paths ("/identities?site=a"); anything
 * else (absolute URLs, protocol-relative "//host", backslash or control
 * character tricks such as "/\t/host", the login page itself) falls back to "/".
 * The result is the normalized path, search and hash.
 */
export function safeRedirect(target: string | undefined): string {
  if (!target || !target.startsWith('/') || target.startsWith('//') || target.startsWith('/\\')) {
    return DEFAULT_REDIRECT;
  }
  let url: URL;
  try {
    url = new URL(target, PROBE_ORIGIN);
  } catch {
    return DEFAULT_REDIRECT;
  }
  // URL parsing strips tabs and newlines and treats "\" like "/", so "/\t/evil" becomes
  // "//evil" (another origin); only targets that stay on the probe origin are safe.
  if (url.origin !== PROBE_ORIGIN) return DEFAULT_REDIRECT;
  if (url.pathname === '/login' || url.pathname.startsWith('/login/')) return DEFAULT_REDIRECT;
  return `${url.pathname}${url.search}${url.hash}`;
}
