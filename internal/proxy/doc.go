// Package proxy manages the outbound proxy pool of a namespace (design doc
// §11, spec §5.4 and §6.8): import and CRUD of proxies with encrypted URLs,
// manual lifecycle operations, provider statistics, URL resolution with
// session templates for lease assignments, and the periodic health checker.
//
// # Storage
//
// PostgreSQL (table proxies) is the source of truth. The full proxy URL,
// including credentials, is sealed with vault.Cipher using the AAD
// vault.AAD(proxyID, "url"); only display_url (no credentials) and
// username_hint (first four characters of the user name followed by "***")
// are stored in clear text. Proxies are de-duplicated per namespace by
// url_hash = HMAC-SHA256(pepper, normalized URL).
//
// The table has no quarantine_until column: as in the action track's state
// writer, ban_until holds the end of a ban (NULL while banned = permanent) or
// of a quarantine, and the action expiry job releases both to active. Leaving
// those states clears it.
//
// # Redis hot state
//
// The hot-state syncer (HotSyncer) materializes the proxy-per-site hashes
// "P:T:px:<p>" and "P:T:pxrdy" from PostgreSQL. This package additionally
// writes the following fields directly (spec §5.4), always adding "p<p>" to
// the site dirty set "P:T:dirty":
//
//   - cd  (site cooldown until, ms)   — OperateProxies cooldown with a site
//   - gcd (global cooldown until, ms) — OperateProxies cooldown without a site,
//     written to every site of the namespace (after proxies.cooldown_until);
//     an existing "P:T:pxrdy" member is moved to max(cd, gcd) with ZADD XX
//     (earlier only when the proxy is not saturated, al < mc)
//   - pid                              — alongside cd/gcd so partial hashes stay attributable
//   - sc sts sn nf lf                  — health EWMA written by the health checker
//     to existing hashes only (alpha 0.1, observation 100 on success and 0 on
//     failure, decay towards baseline 70 with tau 6h) and deleted by the
//     reset_stats operation
//
// # Events
//
// Every lifecycle change publishes an events.TypeProxyState event on
// events.NamespaceChannel(namespaceID) with StateEventData as data:
//
//	{"subject_kind":"proxy","subject_id":"pxy_…","site_id":"","from":"active",
//	 "to":"disabled","action":"disable","until":null,"reason":"…"}
//
// "until" is an RFC 3339 timestamp or null. Besides the lifecycle actions
// (disable, enable, ban, unban, cooldown, quarantine, activate, archive,
// restore, reset_stats, health_check) the action is "update" when UpdateProxy
// changed a proxy or a health check filled its empty region/city from GeoIP
// (from and to are the current state) and "delete" when DeleteProxies removed
// it (from is the last state, to is empty). ImportProxies publishes no events.
// Resolver.Subscribe uses these events to drop cached URLs; attribute changes
// made by imports reach resolvers within ResolverCacheTTL.
package proxy
