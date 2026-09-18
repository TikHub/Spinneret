package vault

import (
	"context"
	"strconv"

	"github.com/Evil0ctal/Spinneret/internal/vault/vaultdb"
)

// rewrapRow is one envelope-encrypted record: its primary key (ID, plus the
// version for versioned tables) and its wrapped DEK.
type rewrapRow struct {
	id         string
	version    int32
	wrappedDEK []byte
	kekID      string
}

// ref identifies the row in error messages (never includes key material).
func (r rewrapRow) ref() string {
	if r.version > 0 {
		return r.id + "@v" + strconv.Itoa(int(r.version))
	}
	return r.id
}

// rewrapBatch holds the rows of one batch with their re-wrapped DEKs.
type rewrapBatch struct {
	rows     []rewrapRow
	wrapped  [][]byte
	newKEKID string
}

func (b rewrapBatch) ids() []string {
	out := make([]string, len(b.rows))
	for i, r := range b.rows {
		out[i] = r.id
	}
	return out
}

func (b rewrapBatch) versions() []int32 {
	out := make([]int32, len(b.rows))
	for i, r := range b.rows {
		out[i] = r.version
	}
	return out
}

func (b rewrapBatch) oldWrapped() [][]byte {
	out := make([][]byte, len(b.rows))
	for i, r := range b.rows {
		out[i] = r.wrappedDEK
	}
	return out
}

func (b rewrapBatch) oldKEKIDs() []string {
	out := make([]string, len(b.rows))
	for i, r := range b.rows {
		out[i] = r.kekID
	}
	return out
}

// rewrapTableSpec describes how to page through and update one table. Rows
// are selected with kek_id != target in primary-key order after the given
// row; updates compare-and-swap on the old KEK id and wrapped DEK so that a
// record re-sealed concurrently is never overwritten.
type rewrapTableSpec struct {
	name   string
	fetch  func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error)
	update func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error)
}

// rewrapTables lists every table holding wrapped DEKs, in processing order.
var rewrapTables = []rewrapTableSpec{
	{
		name: "identity_payloads",
		fetch: func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error) {
			rows, err := q.VaultRewrapIdentityPayloadBatch(ctx, vaultdb.VaultRewrapIdentityPayloadBatchParams{
				CurrentKekID: target, AfterID: after.id, AfterVersion: after.version, LimitRows: limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]rewrapRow, len(rows))
			for i, r := range rows {
				out[i] = rewrapRow{id: r.IdentityID, version: r.Version, wrappedDEK: r.WrappedDek, kekID: r.KekID}
			}
			return out, nil
		},
		update: func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error) {
			return q.VaultRewrapIdentityPayloadUpdate(ctx, vaultdb.VaultRewrapIdentityPayloadUpdateParams{
				NewKekID: b.newKEKID, Ids: b.ids(), Versions: b.versions(),
				OldWrappedDeks: b.oldWrapped(), OldKekIds: b.oldKEKIDs(), NewWrappedDeks: b.wrapped,
			})
		},
	},
	{
		name: "secret_versions",
		fetch: func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error) {
			rows, err := q.VaultRewrapSecretVersionBatch(ctx, vaultdb.VaultRewrapSecretVersionBatchParams{
				CurrentKekID: target, AfterID: after.id, AfterVersion: after.version, LimitRows: limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]rewrapRow, len(rows))
			for i, r := range rows {
				out[i] = rewrapRow{id: r.SecretID, version: r.Version, wrappedDEK: r.WrappedDek, kekID: r.KekID}
			}
			return out, nil
		},
		update: func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error) {
			return q.VaultRewrapSecretVersionUpdate(ctx, vaultdb.VaultRewrapSecretVersionUpdateParams{
				NewKekID: b.newKEKID, Ids: b.ids(), Versions: b.versions(),
				OldWrappedDeks: b.oldWrapped(), OldKekIds: b.oldKEKIDs(), NewWrappedDeks: b.wrapped,
			})
		},
	},
	{
		name: "proxies",
		fetch: func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error) {
			rows, err := q.VaultRewrapProxyBatch(ctx, vaultdb.VaultRewrapProxyBatchParams{
				CurrentKekID: target, AfterID: after.id, LimitRows: limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]rewrapRow, len(rows))
			for i, r := range rows {
				out[i] = rewrapRow{id: r.ID, wrappedDEK: r.WrappedDek, kekID: r.KekID}
			}
			return out, nil
		},
		update: func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error) {
			return q.VaultRewrapProxyUpdate(ctx, vaultdb.VaultRewrapProxyUpdateParams{
				NewKekID: b.newKEKID, Ids: b.ids(), OldWrappedDeks: b.oldWrapped(),
				OldKekIds: b.oldKEKIDs(), NewWrappedDeks: b.wrapped,
			})
		},
	},
	{
		name: "notification_channels",
		fetch: func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error) {
			rows, err := q.VaultRewrapChannelBatch(ctx, vaultdb.VaultRewrapChannelBatchParams{
				CurrentKekID: target, AfterID: after.id, LimitRows: limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]rewrapRow, len(rows))
			for i, r := range rows {
				out[i] = rewrapRow{id: r.ID, wrappedDEK: r.WrappedDek, kekID: r.KekID}
			}
			return out, nil
		},
		update: func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error) {
			return q.VaultRewrapChannelUpdate(ctx, vaultdb.VaultRewrapChannelUpdateParams{
				NewKekID: b.newKEKID, Ids: b.ids(), OldWrappedDeks: b.oldWrapped(),
				OldKekIds: b.oldKEKIDs(), NewWrappedDeks: b.wrapped,
			})
		},
	},
	{
		name: "system_keys",
		fetch: func(ctx context.Context, q *vaultdb.Queries, target string, after rewrapRow, limit int32) ([]rewrapRow, error) {
			rows, err := q.VaultRewrapSystemKeyBatch(ctx, vaultdb.VaultRewrapSystemKeyBatchParams{
				CurrentKekID: target, AfterID: after.id, LimitRows: limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]rewrapRow, len(rows))
			for i, r := range rows {
				out[i] = rewrapRow{id: r.ID, wrappedDEK: r.WrappedDek, kekID: r.KekID}
			}
			return out, nil
		},
		update: func(ctx context.Context, q *vaultdb.Queries, b rewrapBatch) (int64, error) {
			return q.VaultRewrapSystemKeyUpdate(ctx, vaultdb.VaultRewrapSystemKeyUpdateParams{
				NewKekID: b.newKEKID, Ids: b.ids(), OldWrappedDeks: b.oldWrapped(),
				OldKekIds: b.oldKEKIDs(), NewWrappedDeks: b.wrapped,
			})
		},
	},
}
