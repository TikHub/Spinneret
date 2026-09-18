package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Session hash fields (spec §5: P:sess:<sha256(id) hex>). cred is the
// password generation of the user the session was created with (see
// credentialOf); a session whose generation no longer matches the user's is
// invalid, so a password change or reset ends every session created with the
// previous password even when it escaped the revocation of the user's
// session index.
const (
	sessionFieldUser       = "user_id"
	sessionFieldCreated    = "created_ms"
	sessionFieldExpires    = "expires_ms"
	sessionFieldIP         = "ip"
	sessionFieldUA         = "ua"
	sessionFieldCredential = "cred"
)

const (
	// sessionIDBytes is the entropy of a session ID.
	sessionIDBytes = 32
	// sessionIDLen is the base64url (unpadded) length of a session ID.
	sessionIDLen = 43
	// sessionIndexPruneThreshold triggers pruning of a user's session index.
	sessionIndexPruneThreshold = 32
	// maxSessionUserAgent bounds the stored user agent.
	maxSessionUserAgent = 256
)

// refreshSessionScript extends a session only while it still exists, so a
// refresh racing with a logout cannot resurrect a partial hash.
// KEYS[1] session hash key; ARGV[1] new expires_ms; ARGV[2] ttl ms.
var refreshSessionScript = redis.NewScript("auth_session_refresh", `
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'expires_ms', ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

// setSessionCredentialScript moves a session of a user to a new password
// generation; it does nothing when the session no longer exists or belongs to
// another user. KEYS[1] session hash key; ARGV[1] user ID; ARGV[2] generation.
var setSessionCredentialScript = redis.NewScript("auth_session_credential", `
if redis.call('HGET', KEYS[1], 'user_id') ~= ARGV[1] then
  return 0
end
redis.call('HSET', KEYS[1], 'cred', ARGV[2])
return 1
`)

// session is a console session loaded from Redis.
type session struct {
	hash       string
	userID     string
	credential string
	createdMs  int64
	expiresMs  int64
}

// credentialOf returns the password generation of a user: its
// password_changed_at in Unix microseconds (the column's precision), or "0"
// when it was never set.
func credentialOf(passwordChangedAt *time.Time) string {
	if passwordChangedAt == nil {
		return "0"
	}
	return strconv.FormatInt(passwordChangedAt.UnixMicro(), 10)
}

// sessionStore manages console sessions in Redis.
type sessionStore struct {
	rdb  rueidis.Client
	keys redis.Keys
	ttl  time.Duration
}

// sessionHashOf validates a cookie value and returns the hex SHA-256 used as
// the Redis key suffix.
func sessionHashOf(cookie string) (string, bool) {
	if len(cookie) != sessionIDLen {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil || len(raw) != sessionIDBytes {
		return "", false
	}
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:]), true
}

// userIndexKey returns the per-user session index key.
func (s *sessionStore) userIndexKey(userID string) string {
	return s.keys.Session("user:" + userID)
}

// create stores a new session bound to the user's password generation
// credential and returns the cookie value and its hash.
func (s *sessionStore) create(ctx context.Context, userID, credential, ip, ua string, now time.Time) (string, string, error) {
	raw := make([]byte, sessionIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate session id: %w", err)
	}
	cookie := base64.RawURLEncoding.EncodeToString(raw)
	hash, _ := sessionHashOf(cookie)
	key := s.keys.Session(hash)
	idx := s.userIndexKey(userID)
	ttlMs := s.ttl.Milliseconds()
	nowMs := now.UnixMilli()
	cmds := rueidis.Commands{
		s.rdb.B().Hset().Key(key).FieldValue().
			FieldValue(sessionFieldUser, userID).
			FieldValue(sessionFieldCredential, credential).
			FieldValue(sessionFieldCreated, strconv.FormatInt(nowMs, 10)).
			FieldValue(sessionFieldExpires, strconv.FormatInt(nowMs+ttlMs, 10)).
			FieldValue(sessionFieldIP, ip).
			FieldValue(sessionFieldUA, truncateUTF8(ua, maxSessionUserAgent)).
			Build(),
		s.rdb.B().Pexpire().Key(key).Milliseconds(ttlMs).Build(),
		s.rdb.B().Sadd().Key(idx).Member(hash).Build(),
		s.rdb.B().Pexpire().Key(idx).Milliseconds(ttlMs).Build(),
		s.rdb.B().Scard().Key(idx).Build(),
	}
	results := s.rdb.DoMulti(ctx, cmds...)
	for _, r := range results[:4] {
		if err := r.Error(); err != nil {
			return "", "", fmt.Errorf("store session: %w", err)
		}
	}
	if n, err := results[4].AsInt64(); err == nil && n > sessionIndexPruneThreshold {
		// Best effort: stale members only cost memory until the index expires.
		_ = s.pruneIndex(ctx, idx)
	}
	return cookie, hash, nil
}

// pruneIndex removes members whose session no longer exists.
func (s *sessionStore) pruneIndex(ctx context.Context, idx string) error {
	members, err := s.rdb.Do(ctx, s.rdb.B().Smembers().Key(idx).Build()).AsStrSlice()
	if err != nil {
		return fmt.Errorf("list user sessions: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	cmds := make(rueidis.Commands, len(members))
	for i, m := range members {
		cmds[i] = s.rdb.B().Exists().Key(s.keys.Session(m)).Build()
	}
	var stale []string
	for i, r := range s.rdb.DoMulti(ctx, cmds...) {
		if n, err := r.AsInt64(); err == nil && n == 0 {
			stale = append(stale, members[i])
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return s.rdb.Do(ctx, s.rdb.B().Srem().Key(idx).Member(stale...).Build()).Error()
}

// errSessionUnknown reports a missing or malformed session.
var errSessionUnknown = errors.New("auth: unknown session")

// get loads a session by hash.
func (s *sessionStore) get(ctx context.Context, hash string, now time.Time) (session, error) {
	fields, err := s.rdb.Do(ctx, s.rdb.B().Hgetall().Key(s.keys.Session(hash)).Build()).AsStrMap()
	if err != nil {
		return session{}, fmt.Errorf("load session: %w", err)
	}
	sess := session{hash: hash, userID: fields[sessionFieldUser], credential: fields[sessionFieldCredential]}
	sess.createdMs, _ = strconv.ParseInt(fields[sessionFieldCreated], 10, 64)
	sess.expiresMs, err = strconv.ParseInt(fields[sessionFieldExpires], 10, 64)
	// A session without a password generation predates the binding and
	// cannot be checked against password changes, so it is not accepted.
	if sess.userID == "" || sess.credential == "" || err != nil || sess.expiresMs <= now.UnixMilli() {
		return session{}, errSessionUnknown
	}
	return sess, nil
}

// needsRefresh reports whether less than half of the TTL remains.
func (s *sessionStore) needsRefresh(sess session, now time.Time) bool {
	return sess.expiresMs-now.UnixMilli() < s.ttl.Milliseconds()/2
}

// refresh extends a session (sliding expiry) and its user index. It reports
// false when the session no longer exists (for example after a logout).
func (s *sessionStore) refresh(ctx context.Context, sess session, now time.Time) (bool, error) {
	ttlMs := s.ttl.Milliseconds()
	expires := strconv.FormatInt(now.UnixMilli()+ttlMs, 10)
	n, err := refreshSessionScript.Exec(ctx, s.rdb, []string{s.keys.Session(sess.hash)},
		[]string{expires, strconv.FormatInt(ttlMs, 10)}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("refresh session: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if err := s.rdb.Do(ctx, s.rdb.B().Pexpire().Key(s.userIndexKey(sess.userID)).Milliseconds(ttlMs).Build()).Error(); err != nil {
		return true, fmt.Errorf("refresh session index: %w", err)
	}
	return true, nil
}

// delete removes a session and returns its user ID ("" when it did not exist).
func (s *sessionStore) delete(ctx context.Context, hash string) (string, error) {
	key := s.keys.Session(hash)
	userID, err := s.rdb.Do(ctx, s.rdb.B().Hget().Key(key).Field(sessionFieldUser).Build()).ToString()
	if err != nil && !rueidis.IsRedisNil(err) {
		return "", fmt.Errorf("load session: %w", err)
	}
	if err := s.rdb.Do(ctx, s.rdb.B().Del().Key(key).Build()).Error(); err != nil {
		return "", fmt.Errorf("delete session: %w", err)
	}
	if userID != "" {
		if err := s.rdb.Do(ctx, s.rdb.B().Srem().Key(s.userIndexKey(userID)).Member(hash).Build()).Error(); err != nil {
			return userID, fmt.Errorf("update session index: %w", err)
		}
	}
	return userID, nil
}

// setCredential moves the session hash of userID to a new password
// generation (the session kept by a password change). It reports false when
// the session does not exist or belongs to another user.
func (s *sessionStore) setCredential(ctx context.Context, hash, userID, credential string) (bool, error) {
	n, err := setSessionCredentialScript.Exec(ctx, s.rdb, []string{s.keys.Session(hash)},
		[]string{userID, credential}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("update session credential: %w", err)
	}
	return n == 1, nil
}

// revokeUser deletes every session of a user except keepHash ("" = none) and
// returns the number of sessions deleted.
func (s *sessionStore) revokeUser(ctx context.Context, userID, keepHash string) (int, error) {
	idx := s.userIndexKey(userID)
	members, err := s.rdb.Do(ctx, s.rdb.B().Smembers().Key(idx).Build()).AsStrSlice()
	if err != nil {
		return 0, fmt.Errorf("list user sessions: %w", err)
	}
	var cmds rueidis.Commands
	var removed []string
	for _, m := range members {
		if m == keepHash {
			continue
		}
		removed = append(removed, m)
		cmds = append(cmds, s.rdb.B().Del().Key(s.keys.Session(m)).Build())
	}
	if len(removed) > 0 {
		cmds = append(cmds, s.rdb.B().Srem().Key(idx).Member(removed...).Build())
	}
	if len(cmds) == 0 {
		return 0, nil
	}
	deleted := 0
	for i, r := range s.rdb.DoMulti(ctx, cmds...) {
		n, err := r.AsInt64()
		if err != nil {
			return deleted, fmt.Errorf("revoke user sessions: %w", err)
		}
		if i < len(removed) { // DEL results; the last command is the index SREM
			deleted += int(n)
		}
	}
	return deleted, nil
}
