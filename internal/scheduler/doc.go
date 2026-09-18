// Package scheduler implements the lease hot path (spec §6.1): Acquire,
// AcquireBatch, Renew and Release on top of atomic Lua scripts over the Redis
// hot state (spec §5), plus the lease reaper job (spec §6.8).
//
// # Scripts
//
// The scripts live in lua/ and are registered through store/redis.NewScript,
// so the common.lua helpers are available to them. acquire.lua is split by
// concern into several files that are concatenated into one body;
// release.lua and reap.lua share lease_end.lua. Every script takes
// KEYS[1] = "P:{s<site>}:meta"; argument layouts are documented at the top of
// each script.
//
// # Behaviour refinements
//
// The scripts follow spec §6.1 with these refinements, all of which keep the
// frozen key schema and field encodings:
//
//   - Candidate filters are applied lazily: a strategy picks among the sampled
//     candidates using the sampled health entries only, and only the picked
//     candidate is fully evaluated. A filtered candidate is pushed (or removed)
//     exactly as the spec prescribes and the strategy picks again. Removing
//     filtered candidates and re-picking selects with exactly the same
//     distribution as filtering every candidate first, while avoiding the
//     evaluation of all K candidates on every call. weighted_random uses exact
//     rejection sampling (uniform proposals accepted with probability
//     weight/100², then a roulette); pending identities are accepted with
//     probability probe.weight_factor. Scores are clamped to [5, 100].
//   - A quota-exhausted identity is pushed to the earliest time at which the
//     sliding-window estimate admits one more request, never later than the
//     window end.
//   - Pool proxies are sampled in windows of 16 at random offsets (Go-supplied
//     random floats) when more than 48 proxies are available, up to three
//     windows. A proxy that becomes full is pushed in pxrdy to the lease expiry
//     (max_concurrency 1) or by min(ttl, 5 s); releasing restores it. Sampled
//     proxies that cannot serve a lease leave the due range of pxrdy the same
//     way identities do: inactive or missing proxies are removed, cooling ones
//     are pushed to their cooldown end and full ones by min(ttl, 5 s).
//   - Half-open breaker probes ignore sticky sessions (they neither reuse nor
//     rebind the session identity) so that probes always use best_health.
//   - Releasing or expiring an exclusive lease also lowers the score of the
//     identity in the other endpoint groups of its client that pushed it to the
//     exclusive lease expiry while it was leased.
//   - The lease hash field pr marks half-open breaker probes only; the API
//     Lease.probe flag is also set for leases of pending identities.
//   - Renew never shortens a lease; a lease already at its lifetime cap fails
//     with lease_lifetime_exceeded.
//   - A banned account pushes its identities to the ban end (10 minutes for
//     permanent bans and disabled accounts, 10 s for bans awaiting expiry).
//
// Acquire marks e<eg>:<i> and g<i> dirty (last-used and reuse state), releases
// mark reuse changes dirty. Optional Config hooks report lazy open→half_open
// breaker transitions and proxy bindings so their owners can persist them.
package scheduler
