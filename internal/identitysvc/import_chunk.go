package identitysvc

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
)

// chunkTimeout bounds the transaction of one import chunk.
const chunkTimeout = 2 * time.Minute

// chunkWrites collects the writes and effects of one import chunk.
type chunkWrites struct {
	inserts     []identitysvcdb.IdentityCopyParams
	payloads    []identitysvcdb.IdentityPayloadCopyParams
	updates     []identitysvcdb.IdentityWriteParams
	pruneIDs    []string
	pruneFloors []int32
	events      []identitysvcdb.StateEventCopyParams

	result      ImportResult
	resetIDs    []string
	metaIDs     []string
	accountIDs  []string
	transitions []transition
	failures    []ImportFailure
}

// chunkContext carries the fixed inputs of an import chunk.
type chunkContext struct {
	p      *authz.Principal
	ns     *catalog.Namespace
	site   *catalog.Site
	ct     *identity.CompiledType
	mode   string
	dryRun bool
	now    time.Time
	access *secretRefAccess
}

// importChunk writes one chunk in its own transaction (or only reads for a
// dry run) and merges its effects into st after a successful commit.
func (s *Service) importChunk(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site *catalog.Site,
	ct *identity.CompiledType, mode string, dryRun bool, chunk []importRow, access *secretRefAccess, st *importState) error {
	cc := chunkContext{p: p, ns: ns, site: site, ct: ct, mode: mode, dryRun: dryRun, now: s.now().UTC(), access: access}
	tctx, cancel := context.WithTimeout(ctx, chunkTimeout)
	defer cancel()
	var cw *chunkWrites
	var err error
	if dryRun {
		cw, err = s.planChunk(tctx, identitysvcdb.New(s.pool), cc, chunk)
	} else {
		err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
			q := identitysvcdb.New(tx)
			if err := q.IdentityTypeAdvisoryLock(tctx, ct.ID); err != nil {
				return fmt.Errorf("lock identity type %s: %w", ct.ID, err)
			}
			if err := checkTypeVersion(tctx, q, ct); err != nil {
				return err
			}
			planned, err := s.planChunk(tctx, q, cc, chunk)
			if err != nil {
				return err
			}
			if err := applyChunk(tctx, q, planned); err != nil {
				return err
			}
			cw = planned
			return nil
		})
	}
	if err != nil {
		return err
	}
	st.result.Created += cw.result.Created
	st.result.Updated += cw.result.Updated
	st.result.Unchanged += cw.result.Unchanged
	st.resetIDs = append(st.resetIDs, cw.resetIDs...)
	st.metaIDs = append(st.metaIDs, cw.metaIDs...)
	st.accountIDs = append(st.accountIDs, cw.accountIDs...)
	st.transitions = append(st.transitions, cw.transitions...)
	st.failures = append(st.failures, cw.failures...)
	return nil
}

// planChunk looks up existing identities (locking them unless dry run),
// ensures the referenced accounts exist (unless dry run) and computes the
// writes of the chunk.
func (s *Service) planChunk(ctx context.Context, q *identitysvcdb.Queries, cc chunkContext, chunk []importRow) (*chunkWrites, error) {
	existing, err := lookupExisting(ctx, q, cc, chunk)
	if err != nil {
		return nil, err
	}
	cw := &chunkWrites{}
	accountIDs, err := chunkAccounts(ctx, q, cc, chunk, cw)
	if err != nil {
		return nil, err
	}
	actor := cc.p.Actor()
	for i := range chunk {
		row := &chunk[i]
		current, exists := existing[string(row.payload.uniqueHash)]
		if rejected, err := s.rejectSecretRefs(ctx, q, cc, row, current, exists); err != nil {
			return nil, err
		} else if rejected != nil {
			cw.failures = append(cw.failures, ImportFailure{Line: row.line, Message: truncateText(errorMessage(rejected), maxFailureMessageBytes)})
			continue
		}
		switch {
		case !exists:
			cw.result.Created++
			if !cc.dryRun {
				err = s.planCreate(cc, row, accountIDs, actor, cw)
			}
		case cc.mode == ImportModeCreateOnly:
			cw.result.Unchanged++
		default:
			err = s.planExisting(cc, row, current, accountIDs, actor, cw)
		}
		if err != nil {
			return nil, err
		}
	}
	return cw, nil
}

// planExisting adds the writes of a row whose identity already exists: an
// attribute update when only attributes changed, a new payload version (with
// the lifecycle transition) when the payload changed.
func (s *Service) planExisting(cc chunkContext, row *importRow, current lockedIdentity, accountIDs map[string]string,
	actor string, cw *chunkWrites) error {
	w := writeParams(current)
	attrsChanged := applyImportAttributes(&w, row, accountIDs)
	if w.AccountID != nil && attrsChanged && (current.AccountID == nil || *current.AccountID != *w.AccountID) {
		cw.accountIDs = append(cw.accountIDs, *w.AccountID)
	}
	if slices.Equal(current.PayloadHash, row.payload.payloadHash) {
		cw.result.Unchanged++
		if attrsChanged && !cc.dryRun {
			cw.updates = append(cw.updates, w)
			cw.metaIDs = append(cw.metaIDs, w.ID)
		}
		return nil
	}
	cw.result.Updated++
	if cc.dryRun {
		return nil
	}
	t := applyPayloadChange(&w, row.payload, cc.ct.Spec.Activation, cc.now)
	sealed, err := s.sealPayload(w.ID, w.PayloadVersion, row.payload.canonical, actor)
	if err != nil {
		return err
	}
	cw.payloads = append(cw.payloads, sealed)
	cw.updates = append(cw.updates, w)
	cw.pruneIDs = append(cw.pruneIDs, w.ID)
	cw.pruneFloors = append(cw.pruneFloors, pruneFloor(w.PayloadVersion))
	cw.resetIDs = append(cw.resetIDs, w.ID)
	if t != nil {
		cw.transitions = append(cw.transitions, *t)
		cw.events = append(cw.events, stateEventRow(cc.ns.TenantID, cc.ns.ID, cc.site.ID, actor, *t, cc.now))
	}
	return nil
}

// rejectSecretRefs returns the problem (nil when none) of a row referencing
// secrets the caller may not reference: every denied reference of a new
// identity, and the denied references of a changed payload that the same
// field of the identity's stored payload does not hold. Unchanged payloads and
// create_only rows of existing identities write no references. The second
// result is an infrastructure error.
func (s *Service) rejectSecretRefs(ctx context.Context, q *identitysvcdb.Queries, cc chunkContext, row *importRow,
	current lockedIdentity, exists bool) (problem, err error) {
	denied := cc.access.denied(row.payload.secretRefs)
	switch {
	case len(denied) == 0:
		return nil, nil
	case !exists:
		return cc.access.err(denied[0]), nil
	case cc.mode == ImportModeCreateOnly || slices.Equal(current.PayloadHash, row.payload.payloadHash):
		return nil, nil
	}
	if err := s.allowStored(ctx, q, cc.ct, cc.access, current.ID, current.PayloadVersion, row.payload.secretRefFields); err != nil {
		if apperr.ReasonOf(err) == apperr.ReasonInternal {
			return nil, err
		}
		return err, nil
	}
	return nil, nil
}

// lookupExisting returns the existing identities of the chunk by unique hash.
func lookupExisting(ctx context.Context, q *identitysvcdb.Queries, cc chunkContext, chunk []importRow) (map[string]lockedIdentity, error) {
	hashes := make([][]byte, len(chunk))
	for i := range chunk {
		hashes[i] = chunk[i].payload.uniqueHash
	}
	out := make(map[string]lockedIdentity, len(chunk))
	if cc.dryRun {
		rows, err := q.IdentityFindByHashes(ctx, identitysvcdb.IdentityFindByHashesParams{TypeID: cc.ct.ID, Hashes: hashes})
		if err != nil {
			return nil, fmt.Errorf("look up existing identities: %w", err)
		}
		for _, r := range rows {
			out[string(r.UniqueHash)] = lockedIdentity(r)
		}
		return out, nil
	}
	rows, err := q.IdentityLockByHashes(ctx, identitysvcdb.IdentityLockByHashesParams{TypeID: cc.ct.ID, Hashes: hashes})
	if err != nil {
		// Deadlocks and serialization failures become retryable conflicts.
		return nil, pgstore.MapError(err, "identities")
	}
	for _, r := range rows {
		out[string(r.UniqueHash)] = lockedIdentity(r)
	}
	return out, nil
}

// chunkAccounts creates the accounts referenced by the chunk that do not
// exist yet and returns the account IDs by reference. Dry runs only resolve
// nothing (accounts are validated, not created).
func chunkAccounts(ctx context.Context, q *identitysvcdb.Queries, cc chunkContext, chunk []importRow, cw *chunkWrites) (map[string]string, error) {
	var refs []string
	for i := range chunk {
		if chunk[i].account != "" {
			refs = append(refs, chunk[i].account)
		}
	}
	refs = dedupe(refs)
	if len(refs) == 0 || cc.dryRun {
		return map[string]string{}, nil
	}
	// Insert in a stable order so concurrent imports wait on the unique index
	// in the same order instead of deadlocking.
	slices.Sort(refs)
	ids := make([]string, len(refs))
	for i := range refs {
		ids[i] = idgen.New(idgen.Account)
	}
	created, err := q.AccountInsertRefs(ctx, identitysvcdb.AccountInsertRefsParams{SiteID: cc.site.ID, Ids: ids, Refs: refs})
	if err != nil {
		return nil, pgstore.MapError(err, "account")
	}
	for _, a := range created {
		cw.accountIDs = append(cw.accountIDs, a.ID)
	}
	all, err := q.AccountIDsByRefs(ctx, identitysvcdb.AccountIDsByRefsParams{SiteID: cc.site.ID, Refs: refs})
	if err != nil {
		return nil, fmt.Errorf("resolve accounts: %w", err)
	}
	out := make(map[string]string, len(all))
	for _, a := range all {
		out[a.ExternalRef] = a.ID
	}
	for _, ref := range refs {
		if _, ok := out[ref]; !ok {
			return nil, fmt.Errorf("resolve accounts: account %q disappeared concurrently", truncateText(ref, 64))
		}
	}
	return out, nil
}

// planCreate adds the writes of a new identity.
func (s *Service) planCreate(cc chunkContext, row *importRow, accountIDs map[string]string, actor string, cw *chunkWrites) error {
	id := idgen.New(idgen.Identity)
	state := initialState(cc.ct.Spec.Activation)
	var activatedAt *time.Time
	if state == StateActive {
		now := cc.now
		activatedAt = &now
	}
	var accountID *string
	if row.account != "" {
		a := accountIDs[row.account]
		accountID = &a
		cw.accountIDs = append(cw.accountIDs, a)
	}
	tags := row.tags
	if tags == nil {
		tags = []string{}
	}
	sealed, err := s.sealPayload(id, 1, row.payload.canonical, actor)
	if err != nil {
		return err
	}
	cw.inserts = append(cw.inserts, identitysvcdb.IdentityCopyParams{
		ID: id, SiteID: cc.site.ID, Client: cc.ct.Client, TypeID: cc.ct.ID, AccountID: accountID, State: state,
		Region: row.region, Tags: tags, Labels: labelsJSON(row.labels), UniqueHash: row.payload.uniqueHash,
		PayloadHash: row.payload.payloadHash, PayloadVersion: 1, ActivatedAt: activatedAt, CreatedBy: actor,
	})
	cw.payloads = append(cw.payloads, sealed)
	cw.resetIDs = append(cw.resetIDs, id)
	return nil
}

// applyImportAttributes applies the attributes a row sets to w and reports
// whether anything changed. Unset attributes (empty account or region, nil
// tags or labels) keep their stored values.
func applyImportAttributes(w *identitysvcdb.IdentityWriteParams, row *importRow, accountIDs map[string]string) bool {
	changed := false
	if row.account != "" {
		if id, ok := accountIDs[row.account]; ok && (w.AccountID == nil || *w.AccountID != id) {
			w.AccountID = &id
			changed = true
		}
	}
	if row.region != "" && row.region != w.Region {
		w.Region = row.region
		changed = true
	}
	if row.tags != nil && !slices.Equal(row.tags, w.Tags) {
		w.Tags = row.tags
		changed = true
	}
	if row.labels != nil && !sameLabels(w.Labels, row.labels) {
		w.Labels = labelsJSON(row.labels)
		changed = true
	}
	return changed
}

// applyChunk executes the planned writes of a chunk inside its transaction.
func applyChunk(ctx context.Context, q *identitysvcdb.Queries, cw *chunkWrites) error {
	if len(cw.inserts) > 0 {
		if _, err := q.IdentityCopy(ctx, cw.inserts); err != nil {
			return pgstore.MapError(err, "identity")
		}
	}
	if len(cw.payloads) > 0 {
		if _, err := q.IdentityPayloadCopy(ctx, cw.payloads); err != nil {
			return fmt.Errorf("store payloads: %w", err)
		}
	}
	if err := execWrites(ctx, q, cw.updates); err != nil {
		return pgstore.MapError(err, "identity")
	}
	if len(cw.pruneIDs) > 0 {
		if _, err := q.IdentityPayloadPrune(ctx, identitysvcdb.IdentityPayloadPruneParams{
			IdentityIds: cw.pruneIDs, MinVersions: cw.pruneFloors,
		}); err != nil {
			return fmt.Errorf("prune payload versions: %w", err)
		}
	}
	if len(cw.events) > 0 {
		if _, err := q.StateEventCopy(ctx, cw.events); err != nil {
			return fmt.Errorf("record state events: %w", err)
		}
	}
	return nil
}
