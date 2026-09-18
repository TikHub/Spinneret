package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

func TestAuthenticateNoCredentials(t *testing.T) {
	a := NewAuthenticator(Config{}, nil, nil, redisKeysForUnit(), nil, nil)
	p, err := a.Authenticate(context.Background(), request(http.MethodPost, "", "", map[string]string{"Authorization": "Basic Zm9vOmJhcg=="}))
	require.NoError(t, err)
	require.Nil(t, p)
}

func TestAuthenticateTokenFormats(t *testing.T) {
	a := NewAuthenticator(Config{}, nil, nil, redisKeysForUnit(), nil, nil)
	for _, tok := range []string{"", "nope", "spn_short", "spn_" + string(make([]byte, 43))} {
		_, err := a.Authenticate(context.Background(), request(http.MethodPost, tok+" ", "", nil))
		requireReason(t, err, apperr.ReasonTokenInvalid)
	}
}

func TestAuthenticateToken(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	plaintext, id := e.newToken(w, "crawler", CreateTokenInput{Scopes: []string{"lease:acquire:shop", "config:read"}})

	r := request(http.MethodPost, plaintext, "", map[string]string{HeaderNode: "node 7"})
	r.Header.Set("User-Agent", "sdk/1")
	p, err := e.auth.Authenticate(e.ctx(), r)
	require.NoError(t, err)
	require.Equal(t, authz.KindToken, p.Kind)
	require.Equal(t, id, p.ID)
	require.Equal(t, "crawler", p.Name)
	require.Equal(t, w.tenant, p.TenantID)
	require.Equal(t, w.ns, p.NamespaceID)
	require.Equal(t, "prod", p.NamespaceName)
	require.Len(t, p.Scopes, 2)
	require.Equal(t, "203.0.113.7", p.ClientIP)
	require.Equal(t, "sdk/1", p.UserAgent)
	require.Equal(t, "node_7", p.Node)
	require.True(t, p.Can(authz.PermLeaseAcquire, authz.Resource{TenantID: w.tenant, NamespaceID: w.ns, SiteID: w.siteA, SiteName: "shop"}))

	// The verification is cached: deleting the row does not affect it until invalidated.
	_, err = e.pool.Exec(e.ctx(), `DELETE FROM api_tokens WHERE id = $1`, id)
	require.NoError(t, err)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)
	e.auth.InvalidateToken(id)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	requireReason(t, err, apperr.ReasonTokenInvalid)
}

func TestAuthenticateUnknownTokenNegativeCache(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	tok, err := GenerateToken()
	require.NoError(t, err)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, tok, "", nil))
	requireReason(t, err, apperr.ReasonTokenInvalid)
	require.True(t, e.auth.tokens.negative.Contains(string(HashToken(tok))))

	// A token inserted with that hash is still rejected while negatively cached.
	prep, err := prepareToken(CreateTokenInput{Name: "late", Scopes: []string{"admin"}}, time.Now())
	require.NoError(t, err)
	_, err = e.pool.Exec(e.ctx(), `INSERT INTO api_tokens (id, tenant_id, namespace_id, name, token_prefix, token_hash, scopes)
		VALUES ('tok_late', $1, $2, 'late', 'spn_x', $3, $4)`, w.tenant, w.ns, HashToken(tok), prep.scopes)
	require.NoError(t, err)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, tok, "", nil))
	requireReason(t, err, apperr.ReasonTokenInvalid)

	e.auth.tokens.purge()
	p, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, tok, "", nil))
	require.NoError(t, err)
	require.Equal(t, "tok_late", p.ID)
}

func TestAuthenticateTokenExpiredAndCorrupt(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	soon := time.Now().Add(time.Hour)
	plaintext, id := e.newToken(w, "expiring", CreateTokenInput{ExpiresAt: &soon})
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)

	// Expiry is evaluated on every request, including cached verifications.
	e.auth.now = func() time.Time { return soon.Add(time.Second) }
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	requireReason(t, err, apperr.ReasonTokenExpired)
	e.auth.now = time.Now

	corrupt, corruptID := e.newToken(w, "corrupt", CreateTokenInput{})
	_, err = e.pool.Exec(e.ctx(), `UPDATE api_tokens SET scopes = '{"bogus scope"}' WHERE id = $1`, corruptID)
	require.NoError(t, err)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, corrupt, "", nil))
	requireReason(t, err, apperr.ReasonTokenInvalid)
	_ = id
}

func TestAuthenticateTokenIPAllowlist(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	plaintext, _ := e.newToken(w, "restricted", CreateTokenInput{IPAllowlist: []string{"198.51.100.0/24", "2001:db8::1"}})

	tests := []struct {
		name   string
		remote string
		xff    string
		ok     bool
	}{
		{"direct denied", "203.0.113.7:1", "", false},
		{"direct allowed", "198.51.100.9:1", "", true},
		{"ipv6 allowed", "[2001:db8::1]:1", "", true},
		{"forwarded by trusted proxy", "10.0.0.1:1", "198.51.100.20", true},
		{"forwarded by untrusted peer ignored", "203.0.113.7:1", "198.51.100.20", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := request(http.MethodPost, plaintext, "", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			_, err := e.auth.Authenticate(e.ctx(), r)
			if tc.ok {
				require.NoError(t, err)
			} else {
				requireReason(t, err, apperr.ReasonIPNotAllowed)
			}
		})
	}
}

func TestTokenRevocationDropsCache(t *testing.T) {
	e := newEnv(t)
	e.runAuthenticator()
	w := e.world("acme")
	plaintext, id := e.newToken(w, "crawler", CreateTokenInput{})
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)

	_, err = e.tokens.Revoke(e.ctx(), w.ownerP(), id)
	require.NoError(t, err)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	requireReason(t, err, apperr.ReasonTokenRevoked)
}

func TestAuthenticatorEvents(t *testing.T) {
	e := newEnv(t)
	e.runAuthenticator()
	w := e.world("acme")
	plaintext, _ := e.newToken(w, "crawler", CreateTokenInput{})
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)
	require.Equal(t, 1, e.auth.tokens.positive.Len())

	publish := func(typ, data string) {
		require.NoError(t, e.bus.Publish(e.ctx(), events.ChannelTokens, events.Event{Type: typ, Data: []byte(data)}))
	}
	publish("garbage", `[1,2]`)
	require.Equal(t, 1, e.auth.tokens.positive.Len())
	publish(EventTokenRevoked, `{"all":true}`)
	require.Equal(t, 0, e.auth.tokens.positive.Len())

	_, err = e.auth.users.user(e.ctx(), w.owner)
	require.NoError(t, err)
	publish(EventUserChanged, `{"user_id":"`+w.owner+`"}`)
	require.Equal(t, 0, e.auth.users.users.Len())
	_, err = e.auth.users.user(e.ctx(), w.owner)
	require.NoError(t, err)
	publish(EventUserChanged, `{"all":true}`)
	require.Equal(t, 0, e.auth.users.users.Len())
}

func TestTokenLastUsedFlush(t *testing.T) {
	e := newEnv(t)
	e.auth.flushInterval = 20 * time.Millisecond
	w := e.world("acme")
	plaintext, id := e.newToken(w, "crawler", CreateTokenInput{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.auth.Run(ctx) }()

	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var ip string
		var at *time.Time
		if err := e.pool.QueryRow(e.ctx(), `SELECT last_used_at, last_used_ip FROM api_tokens WHERE id = $1`, id).Scan(&at, &ip); err != nil {
			return false
		}
		return at != nil && ip == "203.0.113.7"
	}, 5*time.Second, 20*time.Millisecond)

	// A use recorded right before shutdown is flushed by the final flush.
	later := time.Now().Add(time.Hour)
	e.auth.lastUsed.record(id, later, "198.51.100.1")
	cancel()
	require.NoError(t, <-done)
	var ip string
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT last_used_ip FROM api_tokens WHERE id = $1`, id).Scan(&ip))
	require.Equal(t, "198.51.100.1", ip)
}

func TestLastUsedTrackerBounds(t *testing.T) {
	tr := newLastUsedTracker()
	now := time.Now()
	tr.record("a", now, "1")
	tr.record("a", now.Add(-time.Second), "old")
	require.Equal(t, "1", tr.entries["a"].ip)
	for i := range maxLastUsedEntries {
		tr.entries["t"+strconv.Itoa(i)] = lastUse{}
	}
	tr.record("new", now, "2")
	require.NotContains(t, tr.entries, "new")
	require.EqualValues(t, 1, tr.dropped)
	require.NotEmpty(t, tr.take())
	require.Nil(t, tr.take())
}

func TestLastUsedFlushFailureRequeues(t *testing.T) {
	e := newEnv(t)
	tr := newLastUsedTracker()
	tr.record("tok_x", time.Now(), "1.2.3.4")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, tr.flush(ctx, e.auth.q))
	require.Contains(t, tr.entries, "tok_x")
	require.NoError(t, tr.flush(e.ctx(), e.auth.q))
}

func TestCachesSkipAddsRacingInvalidation(t *testing.T) {
	tc := newTokenCache(nil, time.Minute, slog.New(slog.DiscardHandler))
	gen := tc.generation.Load()
	tc.dropToken("tok_x") // an invalidation lands while a lookup is in flight
	tc.addIfCurrent(gen, func() { tc.positive.Add("h1", &tokenRecord{id: "tok_x"}) })
	require.Zero(t, tc.positive.Len(), "stale lookup result is discarded")
	tc.addIfCurrent(tc.generation.Load(), func() { tc.positive.Add("h1", &tokenRecord{id: "tok_x"}) })
	tc.addIfCurrent(tc.generation.Load(), func() { tc.negative.Add("h2", struct{}{}) })
	require.Equal(t, 1, tc.positive.Len())
	tc.dropToken("tok_other")
	require.Equal(t, 1, tc.positive.Len(), "other tokens stay cached")
	tc.purge()
	require.Zero(t, tc.positive.Len()+tc.negative.Len())

	uc := newUserCache(nil)
	gen = uc.generation.Load()
	uc.dropUser("usr_x")
	uc.addIfCurrent(gen, func() { uc.users.Add("usr_x", &userRecord{id: "usr_x"}) })
	require.Zero(t, uc.users.Len())
	uc.addIfCurrent(uc.generation.Load(), func() { uc.users.Add("usr_x", &userRecord{id: "usr_x"}) })
	uc.addIfCurrent(uc.generation.Load(), func() { uc.tenants.Add("ten_x", true) })
	require.Equal(t, 1, uc.users.Len())
	uc.purge()
	require.Zero(t, uc.users.Len()+uc.tenants.Len())
}
