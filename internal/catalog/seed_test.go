package catalog

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
)

// seeder inserts catalog rows directly for tests.
type seeder struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func newSeeder(t *testing.T, pool *pgxpool.Pool) *seeder {
	return &seeder{t: t, pool: pool}
}

func (s *seeder) exec(sql string, args ...any) {
	s.t.Helper()
	_, err := s.pool.Exec(context.Background(), sql, args...)
	require.NoError(s.t, err)
}

func (s *seeder) tenant(name string) string {
	s.t.Helper()
	id := idgen.New(idgen.Tenant)
	s.exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name)
	return id
}

func (s *seeder) namespace(tenantID, name string) string {
	s.t.Helper()
	id := idgen.New(idgen.Namespace)
	s.exec(`INSERT INTO namespaces (id, tenant_id, name, display_name) VALUES ($1, $2, $3, $4)`, id, tenantID, name, name+" display")
	return id
}

func (s *seeder) site(namespaceID, name string, clients ...string) (string, int64) {
	s.t.Helper()
	id := idgen.New(idgen.Site)
	var key int64
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO sites (id, namespace_id, name, display_name, clients) VALUES ($1, $2, $3, $4, $5) RETURNING hkey`,
		id, namespaceID, name, name, clients).Scan(&key)
	require.NoError(s.t, err)
	return id, key
}

func (s *seeder) group(siteID, client, name string) (string, int64) {
	s.t.Helper()
	id := idgen.New(idgen.EndpointGroup)
	var key int64
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO endpoint_groups (id, site_id, client, name, low_watermark) VALUES ($1, $2, $3, $4, 5) RETURNING hkey`,
		id, siteID, client, name).Scan(&key)
	require.NoError(s.t, err)
	return id, key
}

func (s *seeder) rule(groupID, kind, pattern string, position int) string {
	s.t.Helper()
	id := idgen.New(idgen.URIRule)
	s.exec(`INSERT INTO uri_rules (id, endpoint_group_id, kind, pattern, position) VALUES ($1, $2, $3, $4, $5)`,
		id, groupID, kind, pattern, position)
	return id
}

func (s *seeder) identityTypeYAML(siteID, yamlSpec string, version int) string {
	s.t.Helper()
	spec, err := identity.ParseTypeYAML([]byte(yamlSpec))
	require.NoError(s.t, err)
	raw, err := identity.MarshalTypeJSON(spec)
	require.NoError(s.t, err)
	return s.identityTypeRaw(siteID, spec.Client, spec.Name, raw, version)
}

func (s *seeder) identityTypeRaw(siteID, client, name string, spec []byte, version int) string {
	s.t.Helper()
	id := idgen.New(idgen.IdentityType)
	s.exec(`INSERT INTO identity_types (id, site_id, client, name, spec, version) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, siteID, client, name, spec, version)
	return id
}

// policyYAML creates a policy whose versions are the given YAML documents; the
// last one is published (current_version = len(versions)).
func (s *seeder) policyYAML(namespaceID string, kind policy.Kind, versions ...string) string {
	s.t.Helper()
	raw := make([][]byte, 0, len(versions))
	name := ""
	for _, doc := range versions {
		spec, err := policy.ParseYAML(kind, []byte(doc))
		require.NoError(s.t, err)
		b, err := policy.MarshalJSON(spec)
		require.NoError(s.t, err)
		raw = append(raw, b)
		name = spec.PolicyName()
	}
	return s.policyRaw(namespaceID, kind, name, len(raw), raw...)
}

// policyRaw creates a policy with raw JSON versions and the given current version.
func (s *seeder) policyRaw(namespaceID string, kind policy.Kind, name string, current int, versions ...[]byte) string {
	s.t.Helper()
	id := idgen.New(idgen.Policy)
	s.exec(`INSERT INTO policies (id, namespace_id, kind, name, current_version) VALUES ($1, $2, $3, $4, 0)`,
		id, namespaceID, string(kind), name)
	for i, v := range versions {
		s.exec(`INSERT INTO policy_versions (policy_id, version, spec, spec_yaml) VALUES ($1, $2, $3, '')`, id, i+1, v)
	}
	s.exec(`UPDATE policies SET current_version = $2 WHERE id = $1`, id, current)
	return id
}

func (s *seeder) bind(namespaceID, policyID string, kind policy.Kind, siteID, client, groupID string) {
	s.t.Helper()
	s.exec(`INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id, client, endpoint_group_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''))`,
		idgen.New(idgen.PolicyBinding), policyID, string(kind), namespaceID, siteID, client, groupID)
}

func (s *seeder) deleteNamespace(namespaceID string) {
	s.t.Helper()
	s.exec(`DELETE FROM sites WHERE namespace_id = $1`, namespaceID)
	s.exec(`DELETE FROM namespaces WHERE id = $1`, namespaceID)
}

const webTypeYAML = `
name: web_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
deliver:
  cookie_header: "{{ cookies }}"
`

const appTypeYAML = `
name: app_device
site: shop
client: app
fields:
  device_id: { type: string, required: true }
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
`
