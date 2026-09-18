package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"
)

// refreshLockScript extends the lock TTL only while the caller still owns it.
var refreshLockScript = rueidis.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// unlockScript deletes the lock only while the caller still owns it.
var unlockScript = rueidis.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// ErrInvalidLock is returned by the lock helpers for an empty key, an empty
// owner or a TTL shorter than one millisecond.
var ErrInvalidLock = errors.New("redis: invalid lock arguments")

// TryLock acquires key for owner with SET NX PX. It returns false (and no
// error) when another owner holds the lock.
func TryLock(ctx context.Context, c rueidis.Client, key, owner string, ttl time.Duration) (bool, error) {
	ms, err := lockArgs(key, owner, ttl)
	if err != nil {
		return false, err
	}
	err = c.Do(ctx, c.B().Set().Key(key).Value(owner).Nx().PxMilliseconds(ms).Build()).Error()
	switch {
	case err == nil:
		return true, nil
	case rueidis.IsRedisNil(err):
		return false, nil
	default:
		return false, fmt.Errorf("redis: acquire lock %s: %w", key, err)
	}
}

// RefreshLock resets the TTL of key when it is still held by owner
// (compare-and-pexpire). It returns false when the lock was lost.
func RefreshLock(ctx context.Context, c rueidis.Client, key, owner string, ttl time.Duration) (bool, error) {
	ms, err := lockArgs(key, owner, ttl)
	if err != nil {
		return false, err
	}
	n, err := refreshLockScript.Exec(ctx, c, []string{key}, []string{owner, strconv.FormatInt(ms, 10)}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("redis: refresh lock %s: %w", key, err)
	}
	return n == 1, nil
}

// Unlock releases key when it is still held by owner (compare-and-delete).
// Releasing a lock that expired or belongs to someone else is not an error.
func Unlock(ctx context.Context, c rueidis.Client, key, owner string) error {
	if key == "" || owner == "" {
		return ErrInvalidLock
	}
	if err := unlockScript.Exec(ctx, c, []string{key}, []string{owner}).Error(); err != nil {
		return fmt.Errorf("redis: release lock %s: %w", key, err)
	}
	return nil
}

// lockArgs validates lock arguments and returns the TTL in milliseconds.
func lockArgs(key, owner string, ttl time.Duration) (int64, error) {
	ms := ttl.Milliseconds()
	if key == "" || owner == "" || ms < 1 {
		return 0, ErrInvalidLock
	}
	return ms, nil
}
