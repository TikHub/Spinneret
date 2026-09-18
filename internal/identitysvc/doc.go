// Package identitysvc implements identity administration on top of the pure
// identity type logic in internal/identity: identity type CRUD and delivery
// previews, identities (listing, detail with masked or revealed payloads,
// JSON Lines / CSV imports, payload and attribute updates), accounts, manual
// operations and reverts (delegated to the action track through the Operator
// interface), state event listing, and the decrypted credential cache used by
// the Acquire hot path (PayloadCache).
//
// Authorization: every Service method that takes an *authz.Principal enforces
// the permissions of spec §3.2 itself (identity:read, identity:write,
// identity:operate, identity:reveal on the site of the object; site:write to
// mutate identity types, site:read or identity:read to read them). List
// methods silently narrow results to the sites the principal may access and
// return permission_denied only when the principal cannot access any site of
// the namespace or explicitly names a site it may not access. Objects looked
// up by ID in a tenant or namespace the principal has no relation to are
// reported as not_found, so IDs cannot be probed across tenants.
//
// Consistency: PostgreSQL is the source of truth. Mutations commit first, then
// the catalog is invalidated (identity types) and the hot state is
// synchronized (HotSyncer). Post-commit steps run with a context detached
// from the caller's cancellation; their failures are logged and recorded in
// the audit entry (hot_sync_failed) but do not fail the request, because the
// data is already committed.
//
// Payloads are stored as canonical JSON of the normalized payload, sealed with
// vault envelope encryption under AAD(identityID, "payload:v<version>"). The
// last MaxPayloadVersions versions are kept. unique_hash and payload_hash are
// HMAC-SHA256 values keyed by the dedupe pepper, so the database never holds
// unkeyed digests of credentials. Payload values are never logged and never
// written to audit details.
package identitysvc
