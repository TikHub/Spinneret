package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Login throttle limits (spec §3.3).
const (
	LoginUserFailureLimit = 5
	LoginIPFailureLimit   = 20
	LoginThrottleWindow   = 15 * time.Minute

	rateKindLoginUser = "login_user"
	rateKindLoginIP   = "login_ip"
)

// releaseAttemptScript decrements an attempt counter, deleting it at zero.
// KEYS[1] counter key.
var releaseAttemptScript = redis.NewScript("auth_login_release", `
local n = redis.call('DECR', KEYS[1])
if n <= 0 then
  redis.call('DEL', KEYS[1])
end
return n
`)

// loginThrottle counts login attempts per username and per client IP in Redis
// (P:rl:login_user:<username>, P:rl:login_ip:<ip>).
type loginThrottle struct {
	rdb  rueidis.Client
	keys redis.Keys
}

// counter is one throttle dimension of a login attempt.
type counter struct {
	key   string
	limit int64
}

func (t *loginThrottle) counters(username, ip string) []counter {
	out := []counter{{key: t.keys.RateLimit(rateKindLoginUser, username), limit: LoginUserFailureLimit}}
	if ip != "" {
		out = append(out, counter{key: t.keys.RateLimit(rateKindLoginIP, ip), limit: LoginIPFailureLimit})
	}
	return out
}

// reserve counts an attempt before the password is verified, so concurrent
// guesses cannot exceed the limits. It returns login_throttled when a limit
// was already reached; the attempt is then not counted.
func (t *loginThrottle) reserve(ctx context.Context, username, ip string) error {
	cs := t.counters(username, ip)
	cmds := make(rueidis.Commands, 0, 2*len(cs))
	for _, c := range cs {
		cmds = append(cmds, t.rdb.B().Get().Key(c.key).Build(), t.rdb.B().Pttl().Key(c.key).Build())
	}
	results := t.rdb.DoMulti(ctx, cmds...)
	windowMs := LoginThrottleWindow.Milliseconds()
	for i, c := range cs {
		n, err := results[2*i].AsInt64()
		if err != nil && !rueidis.IsRedisNil(err) {
			return fmt.Errorf("read login throttle: %w", err)
		}
		if n < c.limit {
			continue
		}
		pttl, err := results[2*i+1].AsInt64()
		if err != nil {
			return fmt.Errorf("read login throttle expiry: %w", err)
		}
		if pttl == -1 {
			// The counter lost its expiry (an earlier PEXPIRE failed after
			// INCR); without one the account would stay locked forever.
			if err := t.rdb.Do(ctx, t.rdb.B().Pexpire().Key(c.key).Milliseconds(windowMs).Nx().Build()).Error(); err != nil {
				return fmt.Errorf("restore login throttle expiry: %w", err)
			}
			pttl = windowMs
		}
		return throttledError(pttl)
	}
	cmds = cmds[:0]
	for _, c := range cs {
		cmds = append(cmds,
			t.rdb.B().Incr().Key(c.key).Build(),
			t.rdb.B().Pexpire().Key(c.key).Milliseconds(windowMs).Nx().Build())
	}
	results = t.rdb.DoMulti(ctx, cmds...)
	var exceeded bool
	for i, c := range cs {
		n, err := results[2*i].AsInt64()
		if err != nil {
			return fmt.Errorf("count login attempt: %w", err)
		}
		if err := results[2*i+1].Error(); err != nil {
			return fmt.Errorf("expire login throttle: %w", err)
		}
		if n > c.limit {
			exceeded = true
		}
	}
	if exceeded {
		// A concurrent attempt consumed the last slot; this one stays counted.
		return throttledError(windowMs)
	}
	return nil
}

// succeeded clears the username counter and returns the IP slot.
func (t *loginThrottle) succeeded(ctx context.Context, username, ip string) error {
	if err := t.rdb.Do(ctx, t.rdb.B().Del().Key(t.keys.RateLimit(rateKindLoginUser, username)).Build()).Error(); err != nil {
		return fmt.Errorf("reset login throttle: %w", err)
	}
	if ip == "" {
		return nil
	}
	if err := releaseAttemptScript.Exec(ctx, t.rdb, []string{t.keys.RateLimit(rateKindLoginIP, ip)}, nil).Error(); err != nil {
		return fmt.Errorf("release login throttle: %w", err)
	}
	return nil
}

// reset clears the username counter (after an administrative password reset).
func (t *loginThrottle) reset(ctx context.Context, username string) error {
	if err := t.rdb.Do(ctx, t.rdb.B().Del().Key(t.keys.RateLimit(rateKindLoginUser, username)).Build()).Error(); err != nil {
		return fmt.Errorf("reset login throttle: %w", err)
	}
	return nil
}

func throttledError(retryMs int64) error {
	if retryMs <= 0 {
		retryMs = LoginThrottleWindow.Milliseconds()
	}
	wait := max((time.Duration(retryMs) * time.Millisecond).Round(time.Second), time.Second)
	return apperr.ResourceExhausted(apperr.ReasonLoginThrottled, retryMs,
		"too many failed sign-in attempts, retry in %s", wait)
}
