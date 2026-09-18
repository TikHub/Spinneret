package policysvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
)

// defaultVersionComment is the comment of the version installed for built-in
// default policies.
const defaultVersionComment = "built-in default"

// InstallNamespaceDefaults installs the four built-in default policies
// (default-rotation, default-signal, default-action, default-breaker) as
// published version 1 bound at namespace level, inside the caller's
// transaction (tenancy.NamespaceInstaller). It is idempotent: existing
// policies, versions and namespace-level bindings are left untouched. The
// caller invalidates the catalog after committing.
func (s *Service) InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error {
	if tx == nil {
		return errors.New("install default policies: transaction is nil")
	}
	if namespaceID == "" {
		return errors.New("install default policies: namespace id is empty")
	}
	if actor == "" {
		actor = authz.System("").Actor()
	}
	q := policysvcdb.New(tx)
	for _, kind := range policy.Kinds() {
		if err := installDefault(ctx, q, namespaceID, actor, kind); err != nil {
			return fmt.Errorf("install default %s policy in namespace %s: %w", kind, namespaceID, err)
		}
	}
	return nil
}

func installDefault(ctx context.Context, q *policysvcdb.Queries, namespaceID, actor string, kind policy.Kind) error {
	yamlText := policy.DefaultYAML(kind)
	spec, err := policy.ParseYAML(kind, []byte(yamlText))
	if err != nil {
		return fmt.Errorf("parse built-in default: %w", err)
	}
	specJSON, err := policy.MarshalJSON(spec)
	if err != nil {
		return fmt.Errorf("encode built-in default: %w", err)
	}
	name := policy.DefaultPolicyName(kind)
	ids, err := q.PolicyInsertIfAbsent(ctx, policysvcdb.PolicyInsertIfAbsentParams{
		ID: idgen.New(idgen.Policy), NamespaceID: namespaceID, Kind: string(kind), Name: name,
		Description: specDescription(spec), CreatedBy: actor,
	})
	if err != nil {
		return fmt.Errorf("insert policy: %w", err)
	}
	var policyID string
	if len(ids) == 1 {
		policyID = ids[0]
	} else {
		existing, err := q.PolicyGetByName(ctx, policysvcdb.PolicyGetByNameParams{NamespaceID: namespaceID, Kind: string(kind), Name: name})
		if err != nil {
			return fmt.Errorf("load existing policy: %w", err)
		}
		policyID = existing.ID
	}
	if _, err := q.PolicyVersionInsertIfAbsent(ctx, policysvcdb.PolicyVersionInsertIfAbsentParams{
		PolicyID: policyID, Version: 1, Spec: specJSON, SpecYaml: yamlText, Comment: defaultVersionComment, CreatedBy: actor,
	}); err != nil {
		return fmt.Errorf("insert version: %w", err)
	}
	// Only an existing, never published policy of the same name needs this.
	if err := q.PolicyMarkPublished(ctx, policyID); err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	if _, err := q.BindingInsertIfAbsent(ctx, policysvcdb.BindingInsertIfAbsentParams{
		ID: idgen.New(idgen.PolicyBinding), PolicyID: policyID, Kind: string(kind), NamespaceID: namespaceID, CreatedBy: actor,
	}); err != nil {
		return fmt.Errorf("insert namespace binding: %w", err)
	}
	return nil
}
