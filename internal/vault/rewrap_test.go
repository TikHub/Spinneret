package vault_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/jobs"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

// kekSet builds ciphers sharing key material by KEK id.
type kekSet map[string][]byte

func newKEKSet(t *testing.T, ids ...string) kekSet {
	t.Helper()
	s := kekSet{}
	for _, id := range ids {
		s[id] = vaulttest.RandomKey(t)
	}
	return s
}

// cipher returns a cipher holding the given KEK ids; the last one is current.
func (s kekSet) cipher(t *testing.T, ids ...string) *vault.Cipher {
	t.Helper()
	keys := make(map[string][]byte, len(ids))
	for _, id := range ids {
		keys[id] = append([]byte(nil), s[id]...)
	}
	p, err := vault.NewLocalKEKProvider(keys, ids[len(ids)-1])
	require.NoError(t, err)
	return vault.NewCipher(p, 128, 0)
}

// sealedRecord identifies one seeded envelope for verification.
type sealedRecord struct {
	query     string // returns ciphertext, wrapped_dek, kek_id
	args      []any
	aad       []byte
	plaintext string
}

// seedEncryptedRows writes rows into every table holding wrapped DEKs using
// c and returns how to read them back.
func seedEncryptedRows(t *testing.T, pool *pgxpool.Pool, c *vault.Cipher, n int) []sealedRecord {
	t.Helper()
	ctx := context.Background()
	tenantID := idgen.New(idgen.Tenant)
	ns := seedNamespace(t, pool, tenantID, "prod", true)
	siteID := idgen.New(idgen.Site)
	_, err := pool.Exec(ctx, `INSERT INTO sites (id, namespace_id, name) VALUES ($1, $2, 'demo')`, siteID, ns.ID)
	require.NoError(t, err)
	typeID := idgen.New(idgen.IdentityType)
	_, err = pool.Exec(ctx, `INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, 'web', 'cookie')`, typeID, siteID)
	require.NoError(t, err)

	var records []sealedRecord
	seal := func(plaintext string, aad []byte) vault.Sealed {
		s, err := c.Seal([]byte(plaintext), aad)
		require.NoError(t, err)
		return s
	}
	for i := range n {
		// identity_payloads: two versions per identity.
		identityID := idgen.New(idgen.Identity)
		hash := sha256.Sum256([]byte(identityID))
		_, err := pool.Exec(ctx, `INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash, payload_version)
VALUES ($1, $2, 'web', $3, $4, $4, 2)`, identityID, siteID, typeID, hash[:])
		require.NoError(t, err)
		for v := 1; v <= 2; v++ {
			aad := vault.AAD(identityID, "payload:v"+string(rune('0'+v)))
			pt := `{"cookie":"` + identityID + `"}`
			s := seal(pt, aad)
			_, err := pool.Exec(ctx, `INSERT INTO identity_payloads (identity_id, version, ciphertext, wrapped_dek, kek_id)
VALUES ($1, $2, $3, $4, $5)`, identityID, v, s.Ciphertext, s.WrappedDEK, s.KEKID)
			require.NoError(t, err)
			records = append(records, sealedRecord{
				query: `SELECT ciphertext, wrapped_dek, kek_id FROM identity_payloads WHERE identity_id = $1 AND version = $2`,
				args:  []any{identityID, v}, aad: aad, plaintext: pt,
			})
		}

		// proxies
		proxyID := idgen.New(idgen.Proxy)
		urlHash := sha256.Sum256([]byte(proxyID))
		aad := vault.AAD(proxyID, "url")
		pt := "http://user:pass@proxy-" + proxyID + ":8080"
		s := seal(pt, aad)
		_, err = pool.Exec(ctx, `INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext, url_wrapped_dek, url_kek_id)
VALUES ($1, $2, 'http', 'proxy', 8080, 'http://proxy:8080', $3, $4, $5, $6)`, proxyID, ns.ID, urlHash[:], s.Ciphertext, s.WrappedDEK, s.KEKID)
		require.NoError(t, err)
		records = append(records, sealedRecord{
			query: `SELECT url_ciphertext, url_wrapped_dek, url_kek_id FROM proxies WHERE id = $1`,
			args:  []any{proxyID}, aad: aad, plaintext: pt,
		})

		// notification_channels
		channelID := idgen.New(idgen.Channel)
		aad = vault.AAD(channelID, "config")
		pt = `{"url":"https://hooks.example/` + channelID + `"}`
		s = seal(pt, aad)
		_, err = pool.Exec(ctx, `INSERT INTO notification_channels (id, tenant_id, name, kind, config_ciphertext, config_wrapped_dek, config_kek_id)
VALUES ($1, $2, $3, 'webhook', $4, $5, $6)`, channelID, tenantID, channelID, s.Ciphertext, s.WrappedDEK, s.KEKID)
		require.NoError(t, err)
		records = append(records, sealedRecord{
			query: `SELECT config_ciphertext, config_wrapped_dek, config_kek_id FROM notification_channels WHERE id = $1`,
			args:  []any{channelID}, aad: aad, plaintext: pt,
		})

		// secret_versions through the store.
		store := vault.NewSecretStore(pool, c, nil, nil)
		path := "app/key-" + string(rune('a'+i))
		sec, err := store.Create(ctx, platformAdmin(), ns, vault.CreateSecretInput{Path: path, Value: "secret-" + path})
		require.NoError(t, err)
		records = append(records, sealedRecord{
			query: `SELECT ciphertext, wrapped_dek, kek_id FROM secret_versions WHERE secret_id = $1 AND version = 1`,
			args:  []any{sec.ID}, aad: vault.AAD(sec.ID, "v1"), plaintext: "secret-" + path,
		})

		// system_keys
		name := "key_" + string(rune('a'+i))
		key, err := vault.SystemKey(ctx, pool, c, name, 32)
		require.NoError(t, err)
		records = append(records, sealedRecord{
			query: `SELECT ciphertext, wrapped_dek, kek_id FROM system_keys WHERE name = $1`,
			args:  []any{name}, aad: vault.SystemKeyAAD(name), plaintext: string(key),
		})
	}
	return records
}

func verifyRecords(t *testing.T, pool *pgxpool.Pool, c *vault.Cipher, records []sealedRecord, wantKEK string) {
	t.Helper()
	for _, r := range records {
		var s vault.Sealed
		require.NoError(t, pool.QueryRow(context.Background(), r.query, r.args...).Scan(&s.Ciphertext, &s.WrappedDEK, &s.KEKID))
		require.Equal(t, wantKEK, s.KEKID, "record %v", r.args)
		got, err := c.Open(s, r.aad)
		require.NoError(t, err, "record %v", r.args)
		require.Equal(t, r.plaintext, string(got))
	}
}

func kekInfo(t *testing.T, st vault.KEKStatus, id string) vault.KEKInfo {
	t.Helper()
	for _, k := range st.KEKs {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("kek %q not in status %+v", id, st.KEKs)
	return vault.KEKInfo{}
}

func TestRewrapEveryTable(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k1", "k2")
	records := seedEncryptedRows(t, pool, keks.cipher(t, "k1"), 3)
	total := int64(len(records))
	require.EqualValues(t, 18, total)

	rotated := keks.cipher(t, "k1", "k2")
	rw := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(rw.Close)
	vault.SetRewrapBatchSize(rw, 2)

	before, err := rw.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "k2", before.CurrentKEKID)
	require.False(t, before.Running)
	require.Equal(t, vault.KEKInfo{ID: "k1", Configured: true, WrappedRecords: total}, kekInfo(t, before, "k1"))
	require.Equal(t, vault.KEKInfo{ID: "k2", Current: true, Configured: true}, kekInfo(t, before, "k2"))

	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, rw.Wait(waitCtx))

	after, err := rw.Status(ctx)
	require.NoError(t, err)
	require.False(t, after.Running)
	require.Empty(t, after.LastError)
	require.Equal(t, total, after.Total)
	require.Equal(t, total, after.Done)
	require.NotNil(t, after.LastFinishedAt)
	require.Zero(t, kekInfo(t, after, "k1").WrappedRecords)
	require.Equal(t, total, kekInfo(t, after, "k2").WrappedRecords)

	// Every record is on k2 and decrypts without k1.
	verifyRecords(t, pool, keks.cipher(t, "k2"), records, "k2")

	// Another instance reports the persisted status.
	peer := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(peer.Close)
	peerStatus, err := peer.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, after.Done, peerStatus.Done)
	require.Equal(t, after.Total, peerStatus.Total)
	require.False(t, peerStatus.Running)

	// A second run has nothing to do.
	started, err = rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, rw.Wait(waitCtx))
	again, err := rw.Status(ctx)
	require.NoError(t, err)
	require.Zero(t, again.Total)
	require.Zero(t, again.Done)
}

func TestRewrapConcurrentStart(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k1", "k2")
	seedEncryptedRows(t, pool, keks.cipher(t, "k1"), 2)
	rotated := keks.cipher(t, "k1", "k2")

	rw := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(rw.Close)
	vault.SetRewrapBatchSize(rw, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	vault.SetRewrapAfterBatch(rw, func(string) {
		if first {
			first = false
			close(entered)
			<-release
		}
	})

	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	<-entered

	// Same instance and another instance both refuse to start.
	started, err = rw.Start(ctx)
	require.NoError(t, err)
	require.False(t, started)
	peer := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(peer.Close)
	started, err = peer.Start(ctx)
	require.NoError(t, err)
	require.False(t, started)

	running, err := rw.Status(ctx)
	require.NoError(t, err)
	require.True(t, running.Running)
	require.EqualValues(t, 12, running.Total)
	peerView, err := peer.Status(ctx)
	require.NoError(t, err)
	require.True(t, peerView.Running, "the persisted heartbeat is fresh")

	close(release)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, rw.Wait(waitCtx))
	done, err := peer.Status(ctx)
	require.NoError(t, err)
	require.False(t, done.Running)
	require.EqualValues(t, 12, done.Done)

	// The lock is free again: the peer can run (nothing left to do).
	started, err = peer.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, peer.Wait(waitCtx))
}

func TestRewrapHeartbeatProgress(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k1", "k2")
	records := seedEncryptedRows(t, pool, keks.cipher(t, "k1"), 1)
	rotated := keks.cipher(t, "k1", "k2")

	rw := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(rw.Close)
	vault.SetRewrapBatchSize(rw, 1)
	vault.SetRewrapHeartbeatInterval(rw, 10*time.Millisecond)
	const pauseAfter = 3
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs before rw.Close so a failed assertion cannot hang Close
	var batches atomic.Int32
	vault.SetRewrapAfterBatch(rw, func(string) {
		if batches.Add(1) == pauseAfter {
			<-release
		}
	})

	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)

	// Another instance only sees the persisted heartbeat.
	peer := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(peer.Close)
	require.Eventually(t, func() bool {
		st, err := peer.Status(ctx)
		return err == nil && st.Running && st.Done == pauseAfter && st.Total == int64(len(records))
	}, 10*time.Second, 20*time.Millisecond)

	unblock()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, rw.Wait(waitCtx))
	st, err := peer.Status(ctx)
	require.NoError(t, err)
	require.False(t, st.Running)
	require.EqualValues(t, len(records), st.Done)
	require.EqualValues(t, len(records), st.Total)
	verifyRecords(t, pool, keks.cipher(t, "k2"), records, "k2")
}

func TestRewrapReportsRecordsLeftBehind(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k1", "k2")
	oldCipher := keks.cipher(t, "k1")
	seedEncryptedRows(t, pool, oldCipher, 2)
	rotated := keks.cipher(t, "k1", "k2")

	rw := vault.NewRewrapper(pool, rotated, nil)
	t.Cleanup(rw.Close)
	vault.SetRewrapBatchSize(rw, 1)
	// While the job runs, an instance still configured with k1 as current
	// seals a payload whose key sorts before the keyset cursor.
	const lateID = "idt_00000000000000000000000000000000"
	var (
		once    sync.Once
		seedErr error
	)
	vault.SetRewrapAfterBatch(rw, func(table string) {
		if table != "identity_payloads" {
			return
		}
		once.Do(func() { seedErr = seedLatePayload(ctx, pool, oldCipher, lateID) })
	})

	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, rw.Wait(waitCtx))
	require.NoError(t, seedErr)

	st, err := rw.Status(ctx)
	require.NoError(t, err)
	require.Contains(t, st.LastError, "1 records are still wrapped by a non-current KEK")
	require.EqualValues(t, 1, kekInfo(t, st, "k1").WrappedRecords)

	// Running again finishes the rotation.
	vault.SetRewrapAfterBatch(rw, nil)
	started, err = rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, rw.Wait(waitCtx))
	st, err = rw.Status(ctx)
	require.NoError(t, err)
	require.Empty(t, st.LastError)
	require.Zero(t, kekInfo(t, st, "k1").WrappedRecords)
}

// seedLatePayload inserts an identity with one payload sealed by c, reusing
// the site and identity type of an existing identity.
func seedLatePayload(ctx context.Context, pool *pgxpool.Pool, c *vault.Cipher, identityID string) error {
	var siteID, typeID string
	if err := pool.QueryRow(ctx, `SELECT site_id, type_id FROM identities LIMIT 1`).Scan(&siteID, &typeID); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(identityID))
	if _, err := pool.Exec(ctx, `INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash)
VALUES ($1, $2, 'web', $3, $4, $4)`, identityID, siteID, typeID, hash[:]); err != nil {
		return err
	}
	s, err := c.Seal([]byte(`{"cookie":"late"}`), vault.AAD(identityID, "payload:v1"))
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity_payloads (identity_id, version, ciphertext, wrapped_dek, kek_id)
VALUES ($1, 1, $2, $3, $4)`, identityID, s.Ciphertext, s.WrappedDEK, s.KEKID)
	return err
}

func TestRewrapLockHeldElsewhere(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	locks := jobs.NewPGLocks(pool)
	ok, _, release, err := locks.TryLock(ctx, vault.RewrapLockName)
	require.NoError(t, err)
	require.True(t, ok)

	rw := vault.NewRewrapper(pool, vaulttest.NewCipher(t), nil)
	t.Cleanup(rw.Close)
	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.False(t, started)
	release()

	started, err = rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, rw.Wait(ctx))

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = rw.Start(canceled)
	require.Error(t, err)
}

func TestRewrapUnknownKEK(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k0", "k1", "k2")
	lost := seedEncryptedRows(t, pool, keks.cipher(t, "k0"), 1)
	kept := seedEncryptedRowsInNewTenant(t, pool, keks.cipher(t, "k1"))

	rw := vault.NewRewrapper(pool, keks.cipher(t, "k1", "k2"), nil)
	t.Cleanup(rw.Close)
	st, err := rw.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, vault.KEKInfo{ID: "k0", WrappedRecords: int64(len(lost))}, kekInfo(t, st, "k0"))

	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, rw.Wait(ctx))

	st, err = rw.Status(ctx)
	require.NoError(t, err)
	require.Contains(t, st.LastError, "6 records could not be re-wrapped")
	require.Contains(t, st.LastError, "unknown kek")
	require.EqualValues(t, len(lost)+len(kept), st.Total)
	require.EqualValues(t, len(kept), st.Done)
	require.EqualValues(t, len(lost), kekInfo(t, st, "k0").WrappedRecords)
	require.False(t, kekInfo(t, st, "k0").Configured)
	verifyRecords(t, pool, keks.cipher(t, "k2"), kept, "k2")
}

func seedEncryptedRowsInNewTenant(t *testing.T, pool *pgxpool.Pool, c *vault.Cipher) []sealedRecord {
	t.Helper()
	ctx := context.Background()
	ns := seedNamespace(t, pool, idgen.New(idgen.Tenant), "other", true)
	store := vault.NewSecretStore(pool, c, nil, nil)
	sec, err := store.Create(ctx, platformAdmin(), ns, vault.CreateSecretInput{Path: "kept", Value: "kept-value"})
	require.NoError(t, err)
	return []sealedRecord{{
		query: `SELECT ciphertext, wrapped_dek, kek_id FROM secret_versions WHERE secret_id = $1 AND version = 1`,
		args:  []any{sec.ID}, aad: vault.AAD(sec.ID, "v1"), plaintext: "kept-value",
	}}
}

func TestRewrapStatusStaleAndClose(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	rw := vault.NewRewrapper(pool, vaulttest.NewCipher(t), nil)

	// A job whose instance died keeps "running" in the document; status
	// reports it as interrupted once the heartbeat is stale.
	require.NoError(t, vault.PersistRewrapState(ctx, rw, true, 5, 10, time.Now().Add(-time.Minute), ""))
	st, err := rw.Status(ctx)
	require.NoError(t, err)
	require.False(t, st.Running)
	require.Contains(t, st.LastError, "stopped before finishing")
	require.EqualValues(t, 5, st.Done)

	require.NoError(t, vault.PersistRewrapState(ctx, rw, true, 5, 10, time.Now(), ""))
	st, err = rw.Status(ctx)
	require.NoError(t, err)
	require.True(t, st.Running)

	// A corrupted document is ignored.
	_, err = pool.Exec(ctx, `UPDATE system_settings SET value = '"garbage"' WHERE key = $1`, vault.RewrapStatusKey)
	require.NoError(t, err)
	st, err = rw.Status(ctx)
	require.NoError(t, err)
	require.False(t, st.Running)
	require.Zero(t, st.Done)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- rw.Run(runCtx) }()
	stop()
	require.NoError(t, <-done)
	_, err = rw.Start(ctx)
	require.ErrorIs(t, err, vault.ErrRewrapperClosed)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = rw.Status(canceled)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "context canceled"))
}

func TestRewrapCanceledByClose(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	keks := newKEKSet(t, "k1", "k2")
	seedEncryptedRows(t, pool, keks.cipher(t, "k1"), 1)
	rw := vault.NewRewrapper(pool, keks.cipher(t, "k1", "k2"), nil)
	vault.SetRewrapBatchSize(rw, 1)
	entered := make(chan struct{})
	var once bool
	vault.SetRewrapAfterBatch(rw, func(string) {
		if !once {
			once = true
			close(entered)
			time.Sleep(50 * time.Millisecond)
		}
	})
	started, err := rw.Start(ctx)
	require.NoError(t, err)
	require.True(t, started)
	<-entered
	rw.Close()

	peer := vault.NewRewrapper(pool, keks.cipher(t, "k1", "k2"), nil)
	t.Cleanup(peer.Close)
	st, err := peer.Status(ctx)
	require.NoError(t, err)
	require.False(t, st.Running)
	require.Contains(t, st.LastError, "context canceled")
	require.Less(t, st.Done, st.Total)
}
