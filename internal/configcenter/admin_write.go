package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter/configdb"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// CreateItem creates a config item (config:write; config:publish as well
// when req.Publish is set). The group must not start with "_". A JSON Schema
// may be attached to json and yaml items. Non-empty content is validated
// (syntax, schema, secret reference syntax) and stored as the draft, or
// published as the first version when req.Publish is set (which also requires
// every referenced secret to exist). Empty content without Publish creates an
// item without a draft. The first version is 1, or one above the last version
// of deleted items of the same name (see DeleteItem).
func (s *Service) CreateItem(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req CreateRequest) (Item, error) {
	if err := requireCaller(p, ns); err != nil {
		return Item{}, err
	}
	if err := s.requireMutation(ctx, p, ns, authz.PermConfigWrite, ActionCreate, "", req.Group, req.Key); err != nil {
		return Item{}, err
	}
	if req.Publish {
		if err := s.requireMutation(ctx, p, ns, authz.PermConfigPublish, ActionCreate, "", req.Group, req.Key); err != nil {
			return Item{}, err
		}
	}
	sch, refs, err := validateCreate(req)
	if err != nil {
		return Item{}, err
	}

	id := idgen.New(idgen.ConfigItem)
	actor := p.Actor()
	now := time.Now().UTC()
	h := headerOf(ns, id, req.Group, req.Key, req.Format)
	var version int32
	tctx, cancel := s.opContext(ctx)
	defer cancel()
	err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := configdb.New(tx)
		params := configdb.ConfigInsertItemParams{
			ID: id, NamespaceID: ns.ID, GroupName: req.Group, Key: req.Key, Format: req.Format,
			Schema: schemaDocument(sch), Description: req.Description, CreatedBy: actor,
		}
		if !req.Publish && req.Content != "" {
			params.DraftContent, params.DraftUpdatedBy, params.DraftUpdatedAt = &req.Content, &actor, &now
		}
		if err := q.ConfigInsertItem(tctx, params); err != nil {
			return pgstore.MapError(err, fmt.Sprintf("config item %q", itemName(req.Group, req.Key)))
		}
		if !req.Publish {
			return nil
		}
		if err := checkSecretsExist(tctx, q, ns.ID, refs); err != nil {
			return err
		}
		// Read after the insert: a concurrent deletion of an item with the
		// same name has committed (and raised the floor) once the insert
		// passed the unique constraint.
		floor, err := versionFloor(tctx, q, ns.ID, req.Group, req.Key)
		if err != nil {
			return err
		}
		if version, err = nextVersion(floor); err != nil {
			return err
		}
		return insertVersion(tctx, q, id, version, req.Content, req.Comment, 0, actor, true)
	})
	if err != nil {
		return Item{}, wrapErr(err, "create config item")
	}
	details := map[string]any{"format": req.Format, "has_schema": sch != nil, "published": req.Publish}
	if req.Publish {
		details["version"] = version
	}
	s.record(ctx, p, ns, ActionCreate, id, req.Group, req.Key, audit.ResultOK, details)
	if req.Publish {
		s.announcePublished(ctx, p, h, version, 0)
	}
	return s.itemView(ctx, ns, id)
}

func validateCreate(req CreateRequest) (*compiledSchema, []SecretRef, error) {
	if err := validateCreatableName(req.Group, req.Key); err != nil {
		return nil, nil, err
	}
	if !validFormat(req.Format) {
		return nil, nil, apperr.InvalidArgument("", "format must be one of json, yaml, text")
	}
	if err := validateText("description", req.Description, MaxDescriptionLen); err != nil {
		return nil, nil, err
	}
	if err := validateText("comment", req.Comment, MaxCommentLen); err != nil {
		return nil, nil, err
	}
	sch, err := schemaFor(req.Format, []byte(req.SchemaJSON))
	if err != nil {
		return nil, nil, err
	}
	if req.Content == "" && !req.Publish {
		return sch, nil, nil
	}
	refs, err := validateContent(req.Format, req.Content, sch)
	if err != nil {
		return nil, nil, err
	}
	return sch, refs, nil
}

// SaveDraft stores draft content and optionally a new schema or description
// (config:write). The draft is validated like published content (syntax,
// schema, secret reference syntax; referenced secrets need not exist yet). A
// draft identical to the current published content clears the draft. A
// schema change applies immediately to later publishes.
func (s *Service) SaveDraft(ctx context.Context, p *authz.Principal, req DraftRequest) (Item, error) {
	h, err := s.loadHeader(ctx, p, req.ID)
	if err != nil {
		return Item{}, err
	}
	if err := s.requireMutation(ctx, p, h.NS, authz.PermConfigWrite, ActionSaveDraft, h.ID, h.Group, h.Key); err != nil {
		return Item{}, err
	}
	if err := validateContentSize(req.Content); err != nil {
		return Item{}, err
	}
	if req.Description != nil {
		if err := validateText("description", *req.Description, MaxDescriptionLen); err != nil {
			return Item{}, err
		}
	}
	var cleared bool
	actor := p.Actor()
	now := time.Now().UTC()
	tctx, cancel := s.opContext(ctx)
	defer cancel()
	err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := configdb.New(tx)
		item, err := q.ConfigLockItem(tctx, h.ID)
		if err != nil {
			return pgstore.MapError(err, "config item")
		}
		doc := []byte(item.Schema)
		if req.SchemaJSON != nil {
			doc = []byte(*req.SchemaJSON)
		}
		sch, err := schemaFor(item.Format, doc)
		if err != nil {
			return err
		}
		if _, err := validateContent(item.Format, req.Content, sch); err != nil {
			return err
		}
		description := item.Description
		if req.Description != nil {
			description = *req.Description
		}
		params := configdb.ConfigUpdateDraftParams{
			ID: h.ID, DraftContent: &req.Content, DraftUpdatedBy: &actor, DraftUpdatedAt: &now,
			Schema: schemaDocument(sch), Description: description,
		}
		cleared, err = matchesPublished(tctx, q, item, req.Content)
		if err != nil {
			return err
		}
		if cleared {
			params.DraftContent, params.DraftUpdatedBy, params.DraftUpdatedAt = nil, nil, nil
		}
		return q.ConfigUpdateDraft(tctx, params)
	})
	if err != nil {
		return Item{}, wrapErr(err, "save config draft")
	}
	s.record(ctx, p, h.NS, ActionSaveDraft, h.ID, h.Group, h.Key, audit.ResultOK, map[string]any{
		"schema_changed":      req.SchemaJSON != nil,
		"description_changed": req.Description != nil,
		"draft_cleared":       cleared,
	})
	return s.itemView(ctx, h.NS, h.ID)
}

// matchesPublished reports whether content equals the current published
// version of the item.
func matchesPublished(ctx context.Context, q *configdb.Queries, item configdb.ConfigItem, content string) (bool, error) {
	if item.CurrentVersion == 0 {
		return false, nil
	}
	v, err := q.ConfigGetVersion(ctx, configdb.ConfigGetVersionParams{ItemID: item.ID, Version: item.CurrentVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load current config version: %w", err)
	}
	return v.Content == content, nil
}

// schemaDocument returns the stored schema document (nil = NULL).
func schemaDocument(sch *compiledSchema) json.RawMessage {
	if sch == nil {
		return nil
	}
	return sch.doc
}
