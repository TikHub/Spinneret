package postgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func TestSystemQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	q := db.New(testutil.Postgres(t))

	t.Run("ping", func(t *testing.T) {
		ok, err := q.Ping(ctx)
		require.NoError(t, err)
		require.Equal(t, int32(1), ok)
	})

	t.Run("settings", func(t *testing.T) {
		_, err := q.GetSystemSetting(ctx, "missing")
		require.True(t, apperr.IsNotFound(postgres.MapError(err, "setting")))

		first, err := q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{Key: "ui", Value: json.RawMessage(`{"theme":"dark"}`)})
		require.NoError(t, err)
		require.JSONEq(t, `{"theme":"dark"}`, string(first.Value))

		second, err := q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{Key: "ui", Value: json.RawMessage(`{"theme":"light"}`)})
		require.NoError(t, err)
		require.False(t, second.UpdatedAt.Before(first.UpdatedAt))

		got, err := q.GetSystemSetting(ctx, "ui")
		require.NoError(t, err)
		require.Equal(t, "ui", got.Key)
		require.JSONEq(t, `{"theme":"light"}`, string(got.Value))
	})

	t.Run("keys", func(t *testing.T) {
		pepper := db.InsertSystemKeyIfAbsentParams{
			Name: "dedupe_pepper", Ciphertext: []byte{1, 2, 3}, WrappedDek: []byte{4, 5}, KekID: "k1",
		}
		inserted, err := q.InsertSystemKeyIfAbsent(ctx, pepper)
		require.NoError(t, err)
		require.Equal(t, pepper.Ciphertext, inserted.Ciphertext)
		require.Equal(t, "k1", inserted.KekID)

		// A second insert keeps the original key.
		dup := pepper
		dup.Ciphertext = []byte{9}
		_, err = q.InsertSystemKeyIfAbsent(ctx, dup)
		require.True(t, errors.Is(err, pgx.ErrNoRows))

		stored, err := q.GetSystemKey(ctx, "dedupe_pepper")
		require.NoError(t, err)
		require.Equal(t, []byte{1, 2, 3}, stored.Ciphertext)

		_, err = q.InsertSystemKeyIfAbsent(ctx, db.InsertSystemKeyIfAbsentParams{
			Name: "other", Ciphertext: []byte{7}, WrappedDek: []byte{8}, KekID: "k2",
		})
		require.NoError(t, err)

		onK1, err := q.ListSystemKeysByKEK(ctx, "k1")
		require.NoError(t, err)
		require.Len(t, onK1, 1)
		require.Equal(t, "dedupe_pepper", onK1[0].Name)

		notOnK2, err := q.ListSystemKeysNotOnKEK(ctx, "k2")
		require.NoError(t, err)
		require.Len(t, notOnK2, 1)
		require.Equal(t, "dedupe_pepper", notOnK2[0].Name)

		none, err := q.ListSystemKeysByKEK(ctx, "k9")
		require.NoError(t, err)
		require.NotNil(t, none)
		require.Empty(t, none)

		// Compare-and-swap rewrap.
		n, err := q.UpdateSystemKeyWrap(ctx, db.UpdateSystemKeyWrapParams{
			Name: "dedupe_pepper", WrappedDek: []byte{6, 6}, KekID: "k2", OldKekID: "k1",
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
		n, err = q.UpdateSystemKeyWrap(ctx, db.UpdateSystemKeyWrapParams{
			Name: "dedupe_pepper", WrappedDek: []byte{7, 7}, KekID: "k3", OldKekID: "k1",
		})
		require.NoError(t, err)
		require.Zero(t, n, "stale rewrap must not apply")

		rewrapped, err := q.GetSystemKey(ctx, "dedupe_pepper")
		require.NoError(t, err)
		require.Equal(t, "k2", rewrapped.KekID)
		require.Equal(t, []byte{6, 6}, rewrapped.WrappedDek)
		require.Equal(t, []byte{1, 2, 3}, rewrapped.Ciphertext)
		require.False(t, rewrapped.UpdatedAt.Before(rewrapped.CreatedAt))

		_, err = q.GetSystemKey(ctx, "missing")
		require.True(t, apperr.IsNotFound(postgres.MapError(err, "system key")))
	})
}
