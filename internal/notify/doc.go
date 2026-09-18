// Package notify implements alerting: notification channels (generic
// webhook, Feishu, DingTalk, WeCom and Telegram bots), alert emission with
// Redis-based de-duplication, a bounded asynchronous delivery pool with
// retries, bus-driven alerts and the periodic alert evaluator.
//
// # Channels
//
// Channels belong to a tenant and optionally to one namespace. Their
// kind-specific settings are validated per kind, sealed with the vault cipher
// (AAD = vault.AAD(channelID, "config")) and only ever returned masked
// (see Channel.Config).
//
// # Alerts
//
// Emit de-duplicates an alert with SET P:alert:<tenant>:<kind>:<dedup key> NX
// PX <ttl>, stores it in alert_events, publishes an "alert" event on the
// namespace channel (every namespace of the tenant for tenant-level alerts)
// and enqueues one delivery per matching channel. Deliveries are appended to
// alert_events.deliveries as JSON objects
//
//	{"channel_id","channel_name","ok","error","attempts","at"}
//
// and summarized in notification_channels.last_delivery_at/status.
//
// A channel matches an alert of its tenant when it is enabled, subscribes to
// the alert kind, the alert severity reaches its minimum severity and its
// scope covers the alert: tenant-wide channels receive every alert; namespace
// channels receive the alerts of their namespace and tenant-level alerts (no
// namespace, e.g. report_backlog); site-restricted channels only receive the
// alerts of their sites and alerts that concern no site. A channel whose last
// delivery failed within 5 minutes gets a single attempt per delivery until a
// delivery succeeds.
//
// Masked credentials sent back on update keep their stored values; credentials
// transmitted verbatim (credential headers of webhooks, Telegram bot tokens)
// are only kept while the destination URL (url, api_base) is unchanged.
// Provider error messages are stored with the channel's credentials redacted.
//
// # Bus events consumed (namespace channels "ns:<id>")
//
//   - breaker.transition — data {"site_id","endpoint_group_id","from","to",
//     "trigger","reason","open_until","version"}; "from_state"/"to_state",
//     "site", "endpoint_group", "client" and "v" are accepted as well.
//     closed→open emits breaker_opened (critical), half_open→open emits
//     breaker_reopened (critical), open|half_open→closed emits breaker_closed
//     (info).
//   - identity.state — data {"subject_kind","subject_id","site_id","from","to",
//     "action","until","reason"}; to=expired emits identity_expired (info) with
//     details identity_id, site, site_id, type, client, reason. Only such
//     events take bus queue slots. Because the queue is bounded, the
//     evaluation job also scans state_events for transitions to expired and
//     emits the alerts that were lost (same de-duplication key, one hour
//     window); see identity_expired.go.
//
// # Bus events published
//
//   - alert — data {"id","kind","severity","title","message","namespace_id",
//     "site_id","created_at"}.
package notify
