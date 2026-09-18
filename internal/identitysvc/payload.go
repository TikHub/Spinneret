package identitysvc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/vault"
)

// minPepperBytes is the minimum accepted dedupe pepper length.
const minPepperBytes = 16

// payloadUpdatedReason is the state reason recorded when a payload update
// changes the lifecycle state.
const payloadUpdatedReason = "payload updated"

// checkCrypto verifies the dependencies needed to store payloads.
func (s *Service) checkCrypto() error {
	switch {
	case s.pool == nil:
		return apperr.Internal(errNoPool)
	case s.cipher == nil:
		return apperr.Internal(errNoCipher)
	case len(s.pepper) < minPepperBytes:
		return apperr.Internal(errPepper)
	}
	return nil
}

// preparedPayload is a normalized payload ready to be stored.
type preparedPayload struct {
	// canonical is the canonical JSON plaintext; zero it once sealed.
	canonical   []byte
	uniqueHash  []byte
	payloadHash []byte
	// secretRefs are the distinct secret paths referenced by secret_ref
	// fields, secretRefFields the same references by field name.
	secretRefs      []string
	secretRefFields map[string]string
}

// preparePayload normalizes raw and computes its canonical form and keyed hashes.
func (s *Service) preparePayload(ct *identity.CompiledType, raw map[string]any) (preparedPayload, error) {
	normalized, err := ct.Normalize(raw)
	if err != nil {
		return preparedPayload{}, err
	}
	key, err := ct.UniqueKey(normalized)
	if err != nil {
		return preparedPayload{}, err
	}
	canonical, err := identity.CanonicalJSON(normalized)
	if err != nil {
		return preparedPayload{}, invalid("payload cannot be encoded: %v", err)
	}
	return preparedPayload{
		canonical:       canonical,
		uniqueHash:      s.keyedHash([]byte(ct.ID), key),
		payloadHash:     s.keyedHash(nil, canonical),
		secretRefs:      ct.SecretRefs(normalized),
		secretRefFields: ct.SecretRefFields(normalized),
	}, nil
}

// keyedHash returns HMAC-SHA256(pepper, prefix || 0x00 || data) when prefix
// is non-nil, otherwise HMAC-SHA256(pepper, data).
func (s *Service) keyedHash(prefix, data []byte) []byte {
	m := hmac.New(sha256.New, s.pepper)
	if prefix != nil {
		m.Write(prefix)
		m.Write([]byte{0})
	}
	m.Write(data)
	return m.Sum(nil)
}

// payloadAAD returns the AAD of payload version v of an identity.
func payloadAAD(identityID string, v int32) []byte {
	return vault.AAD(identityID, "payload:v"+strconv.FormatInt(int64(v), 10))
}

// sealPayload encrypts a payload version.
func (s *Service) sealPayload(identityID string, v int32, plaintext []byte, actor string) (identitysvcdb.IdentityPayloadCopyParams, error) {
	sealed, err := s.cipher.Seal(plaintext, payloadAAD(identityID, v))
	if err != nil {
		return identitysvcdb.IdentityPayloadCopyParams{}, apperr.Internal(fmt.Errorf("seal payload of identity %s: %w", identityID, err))
	}
	return identitysvcdb.IdentityPayloadCopyParams{
		IdentityID: identityID, Version: v, Ciphertext: sealed.Ciphertext, WrappedDek: sealed.WrappedDEK,
		KekID: sealed.KEKID, CreatedBy: actor,
	}, nil
}

// openPayload loads and decrypts payload version v of an identity. A missing
// version is not_found. The decrypted bytes are zeroed after decoding.
func openPayload(ctx context.Context, q *identitysvcdb.Queries, c *vault.Cipher, identityID string, v int32) (map[string]any, error) {
	row, err := q.IdentityPayloadGet(ctx, identitysvcdb.IdentityPayloadGetParams{IdentityID: identityID, Version: v})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("payload version %d of identity %s not found", v, identityID)
	}
	if err != nil {
		return nil, fmt.Errorf("load payload of identity %s: %w", identityID, err)
	}
	return decryptPayload(c, identityID, v, vault.Sealed{Ciphertext: row.Ciphertext, WrappedDEK: row.WrappedDek, KEKID: row.KekID})
}

// decryptPayload decrypts and decodes a sealed payload version. The decrypted
// bytes are zeroed after decoding; errors never quote payload content.
func decryptPayload(c *vault.Cipher, identityID string, v int32, sealed vault.Sealed) (map[string]any, error) {
	plaintext, err := c.Open(sealed, payloadAAD(identityID, v))
	if err != nil {
		return nil, fmt.Errorf("decrypt payload of identity %s: %w", identityID, err)
	}
	defer clear(plaintext)
	var payload map[string]any
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		// The error of encoding/json may quote input bytes; do not wrap it.
		return nil, fmt.Errorf("decode payload of identity %s: invalid JSON", identityID)
	}
	return payload, nil
}

// checkTypeVersion fails with conflict when the stored identity type no longer
// has the version of ct, so payloads are never normalized and deduplicated
// with an outdated spec (e.g. a catalog snapshot that did not reload yet).
// Callers hold the type advisory lock, which spec updates changing unique_by
// also take.
func checkTypeVersion(ctx context.Context, q *identitysvcdb.Queries, ct *identity.CompiledType) error {
	v, err := q.IdentityTypeVersion(ctx, ct.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("identity type %q not found", ct.Name)
	}
	if err != nil {
		return fmt.Errorf("load version of identity type %s: %w", ct.ID, err)
	}
	if int(v) != ct.Version {
		return apperr.Conflict("identity type %q was updated concurrently (version %d, expected %d), retry the request",
			ct.Name, v, ct.Version)
	}
	return nil
}

// lockedIdentity is the locked state of an existing identity.
type lockedIdentity = identitysvcdb.IdentityLockRow

// writeParams returns the update parameters that keep row unchanged.
func writeParams(row lockedIdentity) identitysvcdb.IdentityWriteParams {
	return identitysvcdb.IdentityWriteParams{
		ID: row.ID, AccountID: row.AccountID, State: row.State, StateReason: row.StateReason,
		StateChangedAt: row.StateChangedAt, QuarantineUntil: row.QuarantineUntil, Region: row.Region,
		Tags: row.Tags, Labels: row.Labels, UniqueHash: row.UniqueHash, PayloadHash: row.PayloadHash,
		PayloadVersion: row.PayloadVersion, ActivatedAt: row.ActivatedAt,
	}
}

// stateAfterPayloadUpdate returns the lifecycle state after a payload change
// (spec §8): active, pending, quarantined and expired identities are
// re-validated (pending for probe activation, active for immediate); banned,
// disabled and retired identities keep their state.
func stateAfterPayloadUpdate(state, activation string) string {
	switch state {
	case StateActive, StatePending, StateQuarantined, StateExpired:
		if activation == identity.ActivationImmediate {
			return StateActive
		}
		return StatePending
	default:
		return state
	}
}

// initialState returns the state of a new identity.
func initialState(activation string) string {
	if activation == identity.ActivationImmediate {
		return StateActive
	}
	return StatePending
}

// transition is a lifecycle state change of one identity.
type transition struct {
	IdentityID string
	From, To   string
}

// applyPayloadChange bumps the payload version of w to carry p and applies
// the lifecycle transition. It returns the transition when the state changed.
func applyPayloadChange(w *identitysvcdb.IdentityWriteParams, p preparedPayload, activation string, now time.Time) *transition {
	w.PayloadVersion++
	w.PayloadHash = p.payloadHash
	w.UniqueHash = p.uniqueHash
	from := w.State
	to := stateAfterPayloadUpdate(from, activation)
	if to == from {
		return nil
	}
	w.State = to
	w.StateReason = payloadUpdatedReason
	w.StateChangedAt = now
	if from == StateQuarantined {
		w.QuarantineUntil = nil
	}
	if to == StateActive {
		activated := now
		w.ActivatedAt = &activated
	}
	return &transition{IdentityID: w.ID, From: from, To: to}
}

// pruneFloor returns the lowest payload version kept after writing version v.
func pruneFloor(v int32) int32 {
	return max(v-MaxPayloadVersions+1, 1)
}

// stateEventRow builds the state_events row of a payload-driven transition.
func stateEventRow(tenantID, namespaceID, siteID, actor string, t transition, now time.Time) identitysvcdb.StateEventCopyParams {
	return identitysvcdb.StateEventCopyParams{
		ID: idgen.New(idgen.StateEvent), CreatedAt: now, TenantID: tenantID, NamespaceID: namespaceID, SiteID: siteID,
		SubjectKind: "identity", SubjectID: t.IdentityID, FromState: t.From, ToState: t.To,
		Action: ActionPayloadUpdate, Actor: actor, Reason: payloadUpdatedReason,
	}
}

// execWrites runs a batch of identity updates and returns the first error.
func execWrites(ctx context.Context, q *identitysvcdb.Queries, writes []identitysvcdb.IdentityWriteParams) error {
	if len(writes) == 0 {
		return nil
	}
	var first error
	q.IdentityWrite(ctx, writes).Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	return first
}

// sameLabels reports whether stored JSONB labels equal labels.
func sameLabels(stored json.RawMessage, labels map[string]string) bool {
	var current map[string]string
	if len(bytes.TrimSpace(stored)) > 0 {
		if err := json.Unmarshal(stored, &current); err != nil {
			return false
		}
	}
	if len(current) != len(labels) {
		return false
	}
	for k, v := range labels {
		if cv, ok := current[k]; !ok || cv != v {
			return false
		}
	}
	return true
}

// labelsJSON encodes labels as a JSON object ({} when empty).
func labelsJSON(labels map[string]string) json.RawMessage {
	if len(labels) == 0 {
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// decodeLabels decodes stored JSONB labels, ignoring non-string values.
func decodeLabels(stored json.RawMessage) map[string]string {
	out := map[string]string{}
	var raw map[string]any
	if err := json.Unmarshal(stored, &raw); err != nil {
		return out
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
