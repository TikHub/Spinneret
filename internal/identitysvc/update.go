package identitysvc

import (
	"bytes"
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
)

// UpdateIdentityPayload stores a new payload version of an identity
// (identity:write). A payload equal to the stored one creates no version. A
// changed payload re-validates the identity (spec §8), resets its health and
// failure streaks in the hot state and keeps the last MaxPayloadVersions
// versions. Secrets that the new payload references and the stored one does
// not must be readable by the caller and exist (see secretRefAccess).
func (s *Service) UpdateIdentityPayload(ctx context.Context, p *authz.Principal, id string, payload map[string]any) (Identity, error) {
	if err := s.checkCrypto(); err != nil {
		return Identity{}, err
	}
	if payload == nil {
		return Identity{}, invalid("payload is required")
	}
	loc, ref, err := s.locateIdentity(ctx, p, id, authz.PermIdentityWrite)
	if err != nil {
		return Identity{}, err
	}
	queries := identitysvcdb.New(s.pool)
	ct, err := s.compiledType(ctx, queries, ref.Site, loc.TypeID)
	if err != nil {
		return Identity{}, err
	}
	if ct, err = currentType(ctx, queries, ct); err != nil {
		return Identity{}, err
	}
	prep, err := s.preparePayload(ct, payload)
	if err != nil {
		return Identity{}, err
	}
	defer clear(prep.canonical)
	access := newSecretRefAccess(p, ref.Namespace)
	if err := access.check(ctx, queries, prep.secretRefs); err != nil {
		return Identity{}, err
	}

	var (
		changed bool
		version int32
		trans   *transition
		now     = s.now().UTC()
	)
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := identitysvcdb.New(tx)
		if err := q.IdentityTypeAdvisoryLock(ctx, ct.ID); err != nil {
			return fmt.Errorf("lock identity type %s: %w", ct.ID, err)
		}
		if err := checkTypeVersion(ctx, q, ct); err != nil {
			return err
		}
		row, err := q.IdentityLock(ctx, id)
		if err != nil {
			return pgstore.MapError(err, "identity")
		}
		version = row.PayloadVersion
		samePayload := bytes.Equal(row.PayloadHash, prep.payloadHash)
		if samePayload && bytes.Equal(row.UniqueHash, prep.uniqueHash) {
			return nil
		}
		w := writeParams(row)
		if samePayload {
			// The unique_by paths of the type changed since the payload was stored.
			w.UniqueHash = prep.uniqueHash
		} else {
			if err := s.allowStored(ctx, q, ct, access, id, row.PayloadVersion, prep.secretRefFields); err != nil {
				return err
			}
			changed = true
			trans = applyPayloadChange(&w, prep, ct.Spec.Activation, now)
			version = w.PayloadVersion
			sealed, err := s.sealPayload(id, w.PayloadVersion, prep.canonical, p.Actor())
			if err != nil {
				return err
			}
			if _, err := q.IdentityPayloadCopy(ctx, []identitysvcdb.IdentityPayloadCopyParams{sealed}); err != nil {
				return fmt.Errorf("store payload version %d of identity %s: %w", w.PayloadVersion, id, err)
			}
		}
		if err := execWrites(ctx, q, []identitysvcdb.IdentityWriteParams{w}); err != nil {
			if ae, ok := apperr.As(pgstore.MapError(err, "identity")); ok && ae.Reason == apperr.ReasonAlreadyExists {
				return apperr.AlreadyExists("another identity of type %q has the same unique key", ct.Name)
			}
			return fmt.Errorf("update identity %s: %w", id, err)
		}
		if !changed {
			return nil
		}
		if _, err := q.IdentityPayloadPrune(ctx, identitysvcdb.IdentityPayloadPruneParams{
			IdentityIds: []string{id}, MinVersions: []int32{pruneFloor(w.PayloadVersion)},
		}); err != nil {
			return fmt.Errorf("prune payload versions of identity %s: %w", id, err)
		}
		if trans != nil {
			ev := stateEventRow(ref.Namespace.TenantID, ref.Namespace.ID, ref.Site.ID, p.Actor(), *trans, now)
			if _, err := q.StateEventCopy(ctx, []identitysvcdb.StateEventCopyParams{ev}); err != nil {
				return fmt.Errorf("record state event of identity %s: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return Identity{}, err
	}
	details := map[string]any{"site": ref.Site.Name, "payload_version": version, "changed": changed}
	if changed {
		synced := s.syncHot(ctx, ref.Site.ID, []syncGroup{{ids: []string{id}, opts: SyncOptions{ResetHealth: true, ResetFailures: true}}}, nil)
		if !synced {
			details["hot_sync_failed"] = true
		}
	}
	if trans != nil {
		details["from_state"], details["to_state"] = trans.From, trans.To
		s.publishTransitions(ctx, ref.Namespace, ref.Site.ID, []transition{*trans})
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ref.Namespace.TenantID, ref.Namespace.ID, "identity.update_payload",
		"identity", id, "", audit.ResultOK, details))
	return s.identityByID(ctx, ref.Namespace, ref.Site, id)
}

// UpdateIdentity changes the region, tags, labels and account of an identity
// (identity:write). A non-empty account reference attaches the account of
// the site with that reference, creating it when missing; an empty one
// detaches the identity.
func (s *Service) UpdateIdentity(ctx context.Context, p *authz.Principal, id string, in IdentityUpdate) (Identity, error) {
	if s.pool == nil {
		return Identity{}, apperr.Internal(errNoPool)
	}
	if err := validateIdentityUpdate(&in); err != nil {
		return Identity{}, err
	}
	ref, err := s.ResolveIdentity(ctx, p, id, authz.PermIdentityWrite)
	if err != nil {
		return Identity{}, err
	}
	var accounts []string
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := identitysvcdb.New(tx)
		row, err := q.IdentityLock(ctx, id)
		if err != nil {
			return pgstore.MapError(err, "identity")
		}
		w := writeParams(row)
		if in.Region != nil {
			w.Region = *in.Region
		}
		if in.SetTags {
			w.Tags = in.Tags
		}
		if in.SetLabels {
			w.Labels = labelsJSON(in.Labels)
		}
		if in.AccountRef != nil {
			if row.AccountID != nil {
				accounts = append(accounts, *row.AccountID)
			}
			w.AccountID = nil
			if *in.AccountRef != "" {
				accountID, created, err := ensureAccount(ctx, q, ref.Site.ID, *in.AccountRef)
				if err != nil {
					return err
				}
				w.AccountID = &accountID
				if created || row.AccountID == nil || *row.AccountID != accountID {
					accounts = append(accounts, accountID)
				}
			}
		}
		return execWrites(ctx, q, []identitysvcdb.IdentityWriteParams{w})
	})
	if err != nil {
		return Identity{}, pgstore.MapError(err, "identity")
	}
	details := map[string]any{"site": ref.Site.Name, "fields": updatedFields(in)}
	if !s.syncHot(ctx, ref.Site.ID, []syncGroup{{ids: []string{id}}}, accounts) {
		details["hot_sync_failed"] = true
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ref.Namespace.TenantID, ref.Namespace.ID, "identity.update",
		"identity", id, "", audit.ResultOK, details))
	return s.identityByID(ctx, ref.Namespace, ref.Site, id)
}

// validateIdentityUpdate checks and normalizes an attribute update.
func validateIdentityUpdate(in *IdentityUpdate) error {
	if in.Region != nil {
		if err := validateText("region", *in.Region, MaxRegionBytes); err != nil {
			return err
		}
	}
	if in.SetTags {
		tags, err := validateTags(in.Tags)
		if err != nil {
			return err
		}
		if tags == nil {
			tags = []string{}
		}
		in.Tags = tags
	}
	if in.SetLabels {
		if err := validateLabels(in.Labels); err != nil {
			return err
		}
	}
	if in.AccountRef != nil {
		if err := validateAccountRef(*in.AccountRef); err != nil {
			return err
		}
	}
	return nil
}

// updatedFields lists the attributes an update changes, for the audit log.
func updatedFields(in IdentityUpdate) []string {
	var out []string
	if in.Region != nil {
		out = append(out, "region")
	}
	if in.SetTags {
		out = append(out, "tags")
	}
	if in.SetLabels {
		out = append(out, "labels")
	}
	if in.AccountRef != nil {
		out = append(out, "account")
	}
	return out
}

// ensureAccount returns the ID of the account (siteID, ref), creating it when
// missing.
func ensureAccount(ctx context.Context, q *identitysvcdb.Queries, siteID, ref string) (string, bool, error) {
	created, err := q.AccountInsertRefs(ctx, identitysvcdb.AccountInsertRefsParams{
		SiteID: siteID, Ids: []string{idgen.New(idgen.Account)}, Refs: []string{ref},
	})
	if err != nil {
		return "", false, pgstore.MapError(err, "account")
	}
	if len(created) == 1 {
		return created[0].ID, true, nil
	}
	existing, err := q.AccountIDsByRefs(ctx, identitysvcdb.AccountIDsByRefsParams{SiteID: siteID, Refs: []string{ref}})
	if err != nil {
		return "", false, pgstore.MapError(err, "account")
	}
	if len(existing) != 1 {
		return "", false, apperr.Conflict("account %q was modified concurrently, retry the operation", truncateText(ref, 64))
	}
	return existing[0].ID, false, nil
}
