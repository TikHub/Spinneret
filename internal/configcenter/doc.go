// Package configcenter implements the config center (design doc §12, spec
// §10): config items addressed by namespace/group/key, drafts, immutable
// versions, publish and rollback, diffs, the node read API with secret
// reference resolution and the long-poll watch hub.
//
// # Items and versions
//
// Items have a format (json, yaml or text) and an optional JSON Schema
// (draft 2020-12, json and yaml only; external $ref loading is refused).
// Version numbers of a namespace/group/key never repeat: deleting an item
// records its last version in config_version_floors and an item created again
// under the same name publishes above it, because nodes identify content by
// group, key and version only.
// Drafts are validated on save (syntax, schema, reference syntax); publishing
// additionally requires every referenced secret, and pinned secret version,
// to exist in the namespace. Groups starting with "_" are reserved: the
// read-only "_runtime" group exposes "breakers" and "site_switches" from the
// RuntimeProvider. Runtime API versions are provider versions + 1 so that
// they start at 1 like stored items ("0 = holds nothing" for watches).
//
// # Secret references
//
// Content may contain ${secret:<path>} or ${secret:<path>#<version>} where
// path matches ^[a-z0-9][a-z0-9_./-]{0,255}$ without empty, "." or ".."
// segments. Every "${secret:" sequence must be a valid reference (there is
// no escape syntax). Node reads replace references with values read through
// SecretReader (purpose "config:<group>/<key>"); json content receives
// JSON-string escaped values, yaml and text the raw value. Admin reads always
// return raw content. A permission failure on any referenced secret fails
// the whole node request with permission_denied and is audited as
// config.read / denied.
//
// # Watches
//
// The watch hub caches current versions per (namespace, group, key), loaded
// lazily and refreshed on bus events and by a periodic resync of watched
// keys. Versions of one stored item never move backwards in the cache (stale
// reads racing with newer ones are ignored), and a caller holding a newer
// version than the cached one triggers a re-read from the source of truth
// before a change is delivered, so nodes switching between instances are
// never moved back to an older version.
//
// # Permissions
//
// Admin reads need config:read, drafts config:write, publish, rollback and
// delete config:publish, all on the namespace-level resource with
// ConfigGroup set (token scopes may restrict groups by glob). Node reads need
// config:read on every requested group; "_runtime" items are served only
// when the principal can read the "_runtime" group (for tokens the group
// glob must match "_runtime", e.g. an unrestricted config:read scope).
//
// # Events
//
// Publishing and rolling back publish, on events.ChannelConfig, an event of
// type events.TypeConfigPublished with ConfigEventData
// {"ns","group","key","version"}, and on events.NamespaceChannel(ns) an
// events.TypeConfigPublished event with PublishedEventData
// {"item_id","group","key","version","source_version"(rollback only),"actor"}.
// Deleting publishes EventTypeConfigDeleted on events.ChannelConfig with
// version 0. Run consumes events.ChannelConfig and events.ChannelRuntime
// ({"ns","kind","version"}) to refresh watch state; event payload versions
// are never trusted, the source of truth is reloaded.
//
// # Audit
//
// config.create, config.draft.save, config.publish, config.rollback and
// config.delete (result ok, or denied when the permission check fails) and
// config.read (denied, when a referenced secret cannot be read). Contents are
// never written to the audit log.
//
// # Redis
//
// The package reads and writes no Redis keys directly.
package configcenter
