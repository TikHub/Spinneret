// Package site manages sites, endpoint groups and URI rules, and maps request
// paths to endpoint groups with an immutable URI matcher.
//
// # Paths
//
// [NormalizePath] reduces a request URI ("/path?query#fragment" or an
// absolute http(s) URL) to its raw path. Percent-encoding is not decoded, dot
// segments are not resolved and repeated slashes are kept: rules match the
// path exactly as the crawler node sends it.
//
// # Rules and priority
//
// A rule maps a pattern to an endpoint group. Kinds:
//
//   - exact: the path equals the pattern.
//   - template: "/"-separated segments where a segment "{name}" matches any
//     single non-empty segment, e.g. "/api/item/{id}/detail".
//   - prefix: the path starts with the pattern (byte-wise).
//   - regex: an RE2 expression matched anywhere in the path (unanchored; use
//     "^" and "$" to anchor).
//
// [Matcher.Match] applies, in order: exact; template (more literal segments,
// then fewer parameters, then lower position); prefix (longest pattern wins);
// regex (lower position wins); and finally the site's "_default" group. Ties
// in position are broken by rule ID and then by input order, so the result is
// deterministic.
//
// Trailing slashes are significant for every kind: "/a" and "/a/" are
// different paths, the exact rule "/a" does not match "/a/", the template
// "/a/{id}" does not match "/a/1/", and the prefix "/a/" does not match "/a".
package site
