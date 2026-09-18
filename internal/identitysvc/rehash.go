package identitysvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// rehashBatchSize is the number of identities re-keyed per query when the
// unique_by of an identity type changes.
const rehashBatchSize = 500

// uniqueByChanged reports whether the effective unique_by (defaults applied)
// of a stored spec differs from uniqueBy. An undecodable stored spec counts as
// changed.
func uniqueByChanged(storedSpec []byte, uniqueBy []string) bool {
	old, err := identity.ParseTypeJSON(storedSpec)
	if err != nil {
		return true
	}
	return !slices.Equal(old.UniqueBy, uniqueBy)
}

// rehashIdentities recomputes the unique hashes of every identity of the type
// of ct under its unique_by, inside the caller's transaction, and returns the
// number of identities re-keyed. It takes the type advisory lock, so imports
// and payload updates wait for the new hashes. Hashes are first replaced by
// per-identity placeholders so that intermediate states cannot collide; an
// identity whose payload lacks a unique_by value, or two identities sharing a
// key, fail the update with failed_precondition.
func (s *Service) rehashIdentities(ctx context.Context, q *identitysvcdb.Queries, ct *identity.CompiledType) (int64, error) {
	if err := q.IdentityTypeAdvisoryLock(ctx, ct.ID); err != nil {
		return 0, fmt.Errorf("lock identity type %s: %w", ct.ID, err)
	}
	n, err := q.IdentityScrambleUniqueHashes(ctx, ct.ID)
	if err != nil {
		return 0, fmt.Errorf("reset unique hashes of identity type %s: %w", ct.ID, err)
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.checkCrypto(); err != nil {
		return 0, err
	}
	var total int64
	after := ""
	for {
		rows, err := q.IdentityRehashBatch(ctx, identitysvcdb.IdentityRehashBatchParams{
			TypeID: ct.ID, AfterID: after, MaxRows: rehashBatchSize,
		})
		if err != nil {
			return 0, pgstore.MapError(err, "identities")
		}
		if len(rows) == 0 {
			break
		}
		ids := make([]string, 0, len(rows))
		hashes := make([][]byte, 0, len(rows))
		for _, row := range rows {
			hash, err := s.rehashIdentity(ct, row)
			if err != nil {
				return 0, err
			}
			ids = append(ids, row.ID)
			hashes = append(hashes, hash)
		}
		if _, err := q.IdentitySetUniqueHashes(ctx, identitysvcdb.IdentitySetUniqueHashesParams{Ids: ids, Hashes: hashes}); err != nil {
			if ae, ok := apperr.As(pgstore.MapError(err, "identity")); ok && ae.Reason == apperr.ReasonAlreadyExists {
				return 0, apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
					"identities of type %q would share unique keys under the new unique_by", ct.Name)
			}
			return 0, fmt.Errorf("store unique hashes of identity type %s: %w", ct.ID, err)
		}
		total += int64(len(rows))
		after = rows[len(rows)-1].ID
		if len(rows) < rehashBatchSize {
			break
		}
	}
	return total, nil
}

// rehashIdentity computes the unique hash of one identity under ct.
func (s *Service) rehashIdentity(ct *identity.CompiledType, row identitysvcdb.IdentityRehashBatchRow) ([]byte, error) {
	if row.Ciphertext == nil || row.KekID == nil {
		return nil, apperr.Internal(fmt.Errorf("payload version %d of identity %s is missing", row.PayloadVersion, row.ID))
	}
	payload, err := decryptPayload(s.cipher, row.ID, row.PayloadVersion,
		vault.Sealed{Ciphertext: row.Ciphertext, WrappedDEK: row.WrappedDek, KEKID: *row.KekID})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	key, err := ct.UniqueKey(payload)
	if err != nil {
		return nil, apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
			"identity %s has no unique key under the new unique_by: %s", row.ID, errorMessage(err))
	}
	return s.keyedHash([]byte(ct.ID), key), nil
}
