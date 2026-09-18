package redis

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// DefaultPrefix is the key prefix used when none is configured.
const DefaultPrefix = "sp"

// maxRawSessionKey is the longest sticky session key stored verbatim.
const maxRawSessionKey = 64

// Keys builds every Redis key of the hot-state schema (spec §5). All keys of
// one site share the hash tag "{s<siteKey>}" and all keys of one report shard
// share "{r<shard>}", so Lua scripts touching them stay atomic under Redis
// Cluster. The zero value uses DefaultPrefix.
type Keys struct {
	// Prefix is the global key prefix (SPINNERET_REDIS_PREFIX).
	Prefix string
}

// NewKeys returns a key builder for prefix; an empty prefix means DefaultPrefix.
func NewKeys(prefix string) Keys {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return Keys{Prefix: prefix}
}

func (k Keys) prefix() string {
	if k.Prefix == "" {
		return DefaultPrefix
	}
	return k.Prefix
}

// keyBuf is a small append-only builder that produces a key with a single allocation.
type keyBuf struct {
	b []byte
}

func (k Keys) begin(extra int) keyBuf {
	p := k.prefix()
	kb := keyBuf{b: make([]byte, 0, len(p)+extra+32)}
	kb.b = append(kb.b, p...)
	kb.b = append(kb.b, ':')
	return kb
}

// site starts a site-scoped key: "P:{s<site>}:".
func (k Keys) site(siteKey int64, extra int) keyBuf {
	kb := k.begin(extra + 24)
	kb.b = append(kb.b, "{s"...)
	kb.b = strconv.AppendInt(kb.b, siteKey, 10)
	kb.b = append(kb.b, "}:"...)
	return kb
}

// shard starts a shard-scoped key: "P:{r<shard>}:".
func (k Keys) shard(shard int, extra int) keyBuf {
	kb := k.begin(extra + 16)
	kb.b = append(kb.b, "{r"...)
	kb.b = strconv.AppendInt(kb.b, int64(shard), 10)
	kb.b = append(kb.b, "}:"...)
	return kb
}

func (kb keyBuf) str(s string) keyBuf {
	kb.b = append(kb.b, s...)
	return kb
}

func (kb keyBuf) num(n int64) keyBuf {
	kb.b = strconv.AppendInt(kb.b, n, 10)
	return kb
}

func (kb keyBuf) sep() keyBuf {
	kb.b = append(kb.b, ':')
	return kb
}

func (kb keyBuf) String() string {
	return string(kb.b)
}

// SiteTag returns the cluster hash tag of a site, e.g. "{s12}".
func (k Keys) SiteTag(siteKey int64) string {
	return "{s" + strconv.FormatInt(siteKey, 10) + "}"
}

// SiteBase returns the common prefix of all keys of a site, e.g. "sp:{s12}:".
func (k Keys) SiteBase(siteKey int64) string {
	return k.site(siteKey, 0).String()
}

// SiteMeta returns "P:T:meta", the site metadata hash and KEYS[1] of every site script.
func (k Keys) SiteMeta(siteKey int64) string {
	return k.site(siteKey, 4).str("meta").String()
}

// Ready returns "P:T:rdy:<eg>", the ZSET of identity hkey → available-at ms.
func (k Keys) Ready(siteKey, egKey int64) string {
	return k.site(siteKey, 24).str("rdy:").num(egKey).String()
}

// Health returns "P:T:hs:<eg>", the hash of identity hkey → packed health state.
func (k Keys) Health(siteKey, egKey int64) string {
	return k.site(siteKey, 24).str("hs:").num(egKey).String()
}

// Identity returns "P:T:id:<i>", the identity state hash.
func (k Keys) Identity(siteKey, identityKey int64) string {
	return k.site(siteKey, 24).str("id:").num(identityKey).String()
}

// Account returns "P:T:acc:<a>", the account state hash.
func (k Keys) Account(siteKey, accountKey int64) string {
	return k.site(siteKey, 24).str("acc:").num(accountKey).String()
}

// AccountMembers returns "P:T:accm:<a>", the SET of identity hkeys of an account.
func (k Keys) AccountMembers(siteKey, accountKey int64) string {
	return k.site(siteKey, 24).str("accm:").num(accountKey).String()
}

// Lease returns "P:T:ls:<leaseId>", the lease hash.
func (k Keys) Lease(siteKey int64, leaseID string) string {
	return k.site(siteKey, len(leaseID)+3).str("ls:").str(leaseID).String()
}

// LeaseExpiry returns "P:T:lsexp", the ZSET of active lease id → expires ms.
func (k Keys) LeaseExpiry(siteKey int64) string {
	return k.site(siteKey, 5).str("lsexp").String()
}

// Sticky returns "P:T:stk:<eg>:<session>". normalizedSession must already be
// normalized with NormalizeSessionKey.
func (k Keys) Sticky(siteKey, egKey int64, normalizedSession string) string {
	return k.site(siteKey, len(normalizedSession)+24).str("stk:").num(egKey).sep().str(normalizedSession).String()
}

// Quota returns "P:T:q:<eg>:<i>", the quota counter hash.
func (k Keys) Quota(siteKey, egKey, identityKey int64) string {
	return k.site(siteKey, 42).str("q:").num(egKey).sep().num(identityKey).String()
}

// ProxySite returns "P:T:px:<p>", the proxy-per-site hash.
func (k Keys) ProxySite(siteKey, proxyKey int64) string {
	return k.site(siteKey, 24).str("px:").num(proxyKey).String()
}

// ProxyReady returns "P:T:pxrdy", the ZSET of proxy hkey → available-at ms.
func (k Keys) ProxyReady(siteKey int64) string {
	return k.site(siteKey, 5).str("pxrdy").String()
}

// Window returns "P:T:win:<eg>:<bucket>", the breaker window bucket hash.
func (k Keys) Window(siteKey, egKey, bucket int64) string {
	return k.site(siteKey, 44).str("win:").num(egKey).sep().num(bucket).String()
}

// WindowHLL returns "P:T:winh:<eg>:<bucket>", the HyperLogLog of captcha identities.
func (k Keys) WindowHLL(siteKey, egKey, bucket int64) string {
	return k.site(siteKey, 45).str("winh:").num(egKey).sep().num(bucket).String()
}

// Breaker returns "P:T:brk:<eg>", the breaker state hash.
func (k Keys) Breaker(siteKey, egKey int64) string {
	return k.site(siteKey, 24).str("brk:").num(egKey).String()
}

// Counter returns "P:T:cnt:<subj>:<outcome>:<windowMs>". subject is "i<hkey>",
// "a<hkey>" or "p<hkey>".
func (k Keys) Counter(siteKey int64, subject, outcome string, windowMs int64) string {
	return k.site(siteKey, len(subject)+len(outcome)+26).
		str("cnt:").str(subject).sep().str(outcome).sep().num(windowMs).String()
}

// CrossProxy returns "P:T:xa:p:<p>", the ZSET of identity hkey → last risk ms on a proxy.
func (k Keys) CrossProxy(siteKey, proxyKey int64) string {
	return k.site(siteKey, 26).str("xa:p:").num(proxyKey).String()
}

// CrossIdentity returns "P:T:xa:i:<i>", the ZSET of proxy hkey → last risk ms of an identity.
func (k Keys) CrossIdentity(siteKey, identityKey int64) string {
	return k.site(siteKey, 26).str("xa:i:").num(identityKey).String()
}

// Bans returns "P:T:bans:<i>", the ZSET of ban timestamps of an identity.
func (k Keys) Bans(siteKey, identityKey int64) string {
	return k.site(siteKey, 25).str("bans:").num(identityKey).String()
}

// RecentCooldowns returns "P:T:rcd:<eg>", the ZSET of recently applied endpoint cooldowns.
func (k Keys) RecentCooldowns(siteKey, egKey int64) string {
	return k.site(siteKey, 24).str("rcd:").num(egKey).String()
}

// RecentSiteCooldowns returns "P:T:rcds", the ZSET of recently applied
// identity x site cooldowns (used by revert mode "all").
func (k Keys) RecentSiteCooldowns(siteKey int64) string {
	return k.site(siteKey, 4).str("rcds").String()
}

// OpenBreakers returns "P:T:brko", the SET of endpoint group hkeys whose
// breaker is not closed.
func (k Keys) OpenBreakers(siteKey int64) string {
	return k.site(siteKey, 4).str("brko").String()
}

// Checkpoint returns "P:T:ckpt", the hash of shard → last applied stream id.
func (k Keys) Checkpoint(siteKey int64) string {
	return k.site(siteKey, 4).str("ckpt").String()
}

// Dirty returns "P:T:dirty", the SET of entries changed since the last snapshot.
func (k Keys) Dirty(siteKey int64) string {
	return k.site(siteKey, 5).str("dirty").String()
}

// RoundRobin returns "P:T:rr:<eg>", the round-robin cursor.
func (k Keys) RoundRobin(siteKey, egKey int64) string {
	return k.site(siteKey, 23).str("rr:").num(egKey).String()
}

// ActiveGroups returns "P:T:aeg", the ZSET of eg hkey → last activity ms.
func (k Keys) ActiveGroups(siteKey int64) string {
	return k.site(siteKey, 3).str("aeg").String()
}

// SiteLock returns "P:T:lock:<name>", a per-site job lock.
func (k Keys) SiteLock(siteKey int64, name string) string {
	return k.site(siteKey, len(name)+5).str("lock:").str(name).String()
}

// ShardTag returns the cluster hash tag of a report shard, e.g. "{r3}".
func (k Keys) ShardTag(shard int) string {
	return "{r" + strconv.Itoa(shard) + "}"
}

// Stream returns "P:R:stream", the report stream of a shard.
func (k Keys) Stream(shard int) string {
	return k.shard(shard, 6).str("stream").String()
}

// Dedup returns "P:R:dd:<reportId>", the report de-duplication marker.
func (k Keys) Dedup(shard int, reportID string) string {
	return k.shard(shard, len(reportID)+3).str("dd:").str(reportID).String()
}

// ShardOwner returns "P:R:owner", the owning instance of a shard.
func (k Keys) ShardOwner(shard int) string {
	return k.shard(shard, 5).str("owner").String()
}

// Epoch returns "P:meta:epoch", the hot-state epoch id.
func (k Keys) Epoch() string {
	return k.begin(10).str("meta:epoch").String()
}

// Workers returns "P:workers", the ZSET of instance id → heartbeat ms.
func (k Keys) Workers() string {
	return k.begin(7).str("workers").String()
}

// RuntimeVersions returns "P:rtv:<nsId>", the runtime config version hash of a namespace.
func (k Keys) RuntimeVersions(namespaceID string) string {
	return k.begin(len(namespaceID) + 4).str("rtv:").str(namespaceID).String()
}

// CatalogMarks returns "P:catv", the HASH of catalog change marks (namespace ID → counter) that gives admin
// requests read-your-writes consistency across instances (catalog.ChangeMarks).
func (k Keys) CatalogMarks() string {
	return k.begin(4).str("catv").String()
}

// Session returns "P:sess:<hash>", a console session hash.
func (k Keys) Session(hash string) string {
	return k.begin(len(hash) + 5).str("sess:").str(hash).String()
}

// RateLimit returns "P:rl:<kind>:<key>", a rate-limit counter.
func (k Keys) RateLimit(kind, key string) string {
	return k.begin(len(kind) + len(key) + 4).str("rl:").str(kind).sep().str(key).String()
}

// AlertDedup returns "P:alert:<dedupKey>", an alert de-duplication marker.
func (k Keys) AlertDedup(key string) string {
	return k.begin(len(key) + 6).str("alert:").str(key).String()
}

// Lock returns "P:lock:<name>", a global lock.
func (k Keys) Lock(name string) string {
	return k.begin(len(name) + 5).str("lock:").str(name).String()
}

// Channel returns "P:ch:<name>", an event bus Pub/Sub channel.
func (k Keys) Channel(name string) string {
	return k.begin(len(name) + 3).str("ch:").str(name).String()
}

// ChannelPattern returns "P:ch:*", the PSUBSCRIBE pattern matching every bus
// channel. Glob metacharacters in the prefix are escaped so that the pattern
// never matches the channels of another prefix.
func (k Keys) ChannelPattern() string {
	return escapeGlob(k.prefix()) + ":ch:*"
}

// escapeGlob backslash-escapes the Redis glob metacharacters * ? [ ] \ in s.
func escapeGlob(s string) string {
	if !strings.ContainsAny(s, `*?[]\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// NormalizeSessionKey maps a client-supplied sticky session key to the form
// stored in "P:T:stk:<eg>:<session>": the raw key when it is at most 64
// characters of [A-Za-z0-9_.:-], otherwise the first 32 hex characters of its
// SHA-256 digest.
func NormalizeSessionKey(s string) string {
	if len(s) <= maxRawSessionKey && isRawSessionKey(s) {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func isRawSessionKey(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}
