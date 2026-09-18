package vault

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/vault/vaultdb"
)

// Secret limits.
const (
	// MaxSecretPathLength is the maximum length in bytes of a secret path.
	MaxSecretPathLength = 256
	// MaxSecretValueBytes is the maximum size in bytes of a secret value.
	MaxSecretValueBytes = 65536
	// MaxSecretDescriptionLength is the maximum length in runes of a description.
	MaxSecretDescriptionLength = 1024
	// MaxSecretTags is the maximum number of tags of a secret.
	MaxSecretTags = 64
	// MaxSecretTagLength is the maximum length in runes of one tag.
	MaxSecretTagLength = 64
	// MaxSecretSearchLength is the maximum length in runes of a list search term.
	MaxSecretSearchLength = 256
	// DefaultSecretPageSize is the page size used when a list limit is not positive.
	DefaultSecretPageSize = 50
	// MaxSecretPageSize is the largest page returned by list operations.
	MaxSecretPageSize = 500
	// MaxSecretPurposeLength bounds the purpose recorded with secret reads.
	MaxSecretPurposeLength = 64
)

// Audit vocabulary of secret operations.
const (
	AuditResourceSecret     = "secret"
	AuditActionSecretCreate = "secret.create"
	AuditActionSecretUpdate = "secret.update"
	AuditActionSecretDelete = "secret.delete"
	AuditActionSecretReveal = "secret.reveal"
	AuditActionSecretRead   = "secret.read"
)

// SecretMask is the masked representation of a secret value; values longer
// than eight runes additionally show their last four runes.
const SecretMask = "••••" //nolint:gosec // G101: a display mask, not a credential.

const (
	secretOpTimeout     = 10 * time.Second
	resolveCacheSize    = 4096
	resolveCacheBytes   = 16 << 20
	resolveCacheTTL     = 30 * time.Second
	accessFlushInterval = 10 * time.Second
	accessFlushTimeout  = 10 * time.Second
	maxPendingAccesses  = 100_000
	accessFlushChunk    = 1000
	// accessLogLookback widens the audit scan window below the secret creation
	// time to absorb clock skew between application instances and PostgreSQL.
	accessLogLookback  = 24 * time.Hour
	defaultReadPurpose = "api"
)

// SecretValue is a decrypted secret version.
type SecretValue struct {
	Path      string
	Version   int
	Value     string
	ExpiresAt *time.Time
}

// Secret is the metadata of a secret. It never carries the plaintext value;
// MaskedValue shows at most the last four runes of the current version.
type Secret struct {
	ID             string
	TenantID       string
	NamespaceID    string
	NamespaceName  string
	Path           string
	Description    string
	Tags           []string
	CurrentVersion int
	MaskedValue    string
	ExpiresAt      *time.Time
	LastAccessedAt *time.Time
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SecretStore is the secrets domain service: CRUD with versioning, masked
// listing, audited reveals and reads, and secret resolution for identity
// payload rendering. It is safe for concurrent use.
type SecretStore struct {
	pool    *pgxpool.Pool
	q       *vaultdb.Queries
	cipher  *Cipher
	audit   audit.Recorder
	logger  *slog.Logger
	now     func() time.Time
	resolve *resolveCache
	flight  singleflight.Group
	access  *accessTracker
	// flushEvery is the period of Run's last-access flushes.
	flushEvery time.Duration

	// afterResolveLoad is a test hook run between the database read and the
	// cache insert of ResolveForIdentity.
	afterResolveLoad func()
}

// NewSecretStore returns a secret store. rec receives an entry for every
// mutation, reveal and node read (nil discards them). Run must be started to
// persist last-access times.
func NewSecretStore(pool *pgxpool.Pool, c *Cipher, rec audit.Recorder, logger *slog.Logger) *SecretStore {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SecretStore{
		pool:    pool,
		q:       vaultdb.New(pool),
		cipher:  c,
		audit:   rec,
		logger:  logger.With(slog.String("component", "vault.secrets")),
		now:     time.Now,
		resolve: newResolveCache(resolveCacheSize, resolveCacheBytes, resolveCacheTTL),
		access:  newAccessTracker(maxPendingAccesses),

		flushEvery: accessFlushInterval,
	}
}

// ValidateSecretPath checks a namespace-relative secret path: it must match
// ^[a-z0-9][a-z0-9_./-]{0,255}$ and must not contain empty, "." or ".."
// segments nor end with "/". Errors are apperr InvalidArgument errors.
func ValidateSecretPath(path string) error {
	if path == "" {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret path is required")
	}
	if len(path) > MaxSecretPathLength {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret path exceeds %d bytes", MaxSecretPathLength)
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		lowerAlnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 && !lowerAlnum {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				"secret path must start with a lower-case letter or digit")
		}
		if !lowerAlnum && c != '_' && c != '.' && c != '/' && c != '-' {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				"secret path may only contain lower-case letters, digits, '_', '.', '/' and '-'")
		}
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				`secret path must not contain empty, "." or ".." segments`)
		}
	}
	return nil
}

// MaskSecretValue returns SecretMask followed by the last four runes of v, or
// SecretMask alone when v has at most eight runes.
func MaskSecretValue(v string) string {
	if utf8.RuneCountInString(v) <= 8 {
		return SecretMask
	}
	i := len(v)
	for range 4 {
		_, size := utf8.DecodeLastRuneInString(v[:i])
		i -= size
	}
	return SecretMask + v[i:]
}

// secretVersionAAD binds a secret version ciphertext to its record and version.
func secretVersionAAD(secretID string, version int) []byte {
	return AAD(secretID, "v"+strconv.Itoa(version))
}

func validateSecretValue(v string) error {
	switch {
	case v == "":
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret value must not be empty")
	case len(v) > MaxSecretValueBytes:
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret value exceeds %d bytes", MaxSecretValueBytes)
	case !utf8.ValidString(v):
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret value must be valid UTF-8")
	}
	return nil
}

func validateDescription(d string) error {
	if !utf8.ValidString(d) {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "description must be valid UTF-8")
	}
	if utf8.RuneCountInString(d) > MaxSecretDescriptionLength {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument,
			"description exceeds %d characters", MaxSecretDescriptionLength)
	}
	return nil
}

// normalizeTags validates tags and removes duplicates, keeping the first
// occurrence order. The result is never nil.
func normalizeTags(tags []string, maxTags int) ([]string, error) {
	if len(tags) > maxTags {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d tags are allowed", maxTags)
	}
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		if t == "" || !utf8.ValidString(t) || utf8.RuneCountInString(t) > MaxSecretTagLength ||
			strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				"tags must be 1-%d printable characters", MaxSecretTagLength)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// sanitizePurpose bounds and cleans the purpose recorded with a read.
func sanitizePurpose(purpose string) string {
	purpose = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, purpose)
	if purpose == "" {
		return defaultReadPurpose
	}
	if len(purpose) > MaxSecretPurposeLength {
		cut := MaxSecretPurposeLength
		for cut > 0 && !utf8.RuneStart(purpose[cut]) {
			cut--
		}
		purpose = purpose[:cut]
	}
	return purpose
}

// pageLimit clamps a requested page size.
func pageLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultSecretPageSize
	case n > MaxSecretPageSize:
		return MaxSecretPageSize
	default:
		return n
	}
}

// secretResource builds the authorization resource of a namespace-level secret.
func secretResource(tenantID, namespaceID, namespaceName, path string) authz.Resource {
	return authz.Resource{TenantID: tenantID, NamespaceID: namespaceID, NamespaceName: namespaceName, SecretPath: path}
}

// authorizeSecret checks that the principal holds at least one of perms on an
// existing secret addressed by ID. A principal that cannot see the secret at
// all (neither secret:list nor any of perms) gets not_found, so IDs of other
// tenants or namespaces cannot be probed; a principal that can see it but
// lacks every perm gets the permission error of the first one.
func authorizeSecret(p *authz.Principal, res authz.Resource, perms ...authz.Permission) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	for _, perm := range perms {
		if p.Can(perm, res) {
			return nil
		}
	}
	if len(perms) == 0 || !p.Can(authz.PermSecretList, res) {
		return apperr.NotFound("secret not found")
	}
	return p.Require(perms[0], res)
}

// record writes an audit entry about a secret.
func (s *SecretStore) record(ctx context.Context, p *authz.Principal, tenantID, namespaceID, action, secretID, path, result string, details map[string]any) {
	s.audit.Record(ctx, audit.FromPrincipal(p, tenantID, namespaceID, action, AuditResourceSecret, secretID, path, result, details))
}

// inTx runs fn in a READ COMMITTED transaction.
func (s *SecretStore) inTx(ctx context.Context, fn func(q *vaultdb.Queries) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return fn(s.q.WithTx(tx))
	})
}

// maskEnvelope decrypts the current version envelope (using the DEK cache)
// and masks it. A value that cannot be decrypted degrades to SecretMask; the
// error is returned for the caller to log (it carries no secret material).
func (s *SecretStore) maskEnvelope(secretID string, version int32, ciphertext, wrapped []byte, kekID *string) (string, error) {
	if len(ciphertext) == 0 || kekID == nil {
		return SecretMask, nil
	}
	plaintext, err := s.cipher.Open(Sealed{Ciphertext: ciphertext, WrappedDEK: wrapped, KEKID: *kekID},
		secretVersionAAD(secretID, int(version)))
	if err != nil {
		return SecretMask, err
	}
	defer clear(plaintext)
	return MaskSecretValue(string(plaintext)), nil
}

// maskFailures summarizes values that could not be masked in one operation so
// that listing many undecryptable secrets (for example after a KEK was
// removed too early) logs one line instead of one per secret.
type maskFailures struct {
	count     int
	firstID   string
	firstErr  error
	namespace string
}

func (m *maskFailures) add(secretID string, err error) {
	if err == nil {
		return
	}
	if m.count == 0 {
		m.firstID, m.firstErr = secretID, err
	}
	m.count++
}

func (m *maskFailures) log(logger *slog.Logger) {
	if m.count == 0 {
		return
	}
	logger.Warn("mask secret: decrypt current version failed",
		slog.String("namespace_id", m.namespace), slog.Int("failed", m.count),
		slog.String("first_secret_id", m.firstID), slog.Any("error", m.firstErr))
}

func (s *SecretStore) secretFromGetRow(row vaultdb.VaultSecretGetRow) Secret {
	masked, err := s.maskEnvelope(row.ID, row.CurrentVersion, row.Ciphertext, row.WrappedDek, row.KekID)
	failures := maskFailures{namespace: row.NamespaceID}
	failures.add(row.ID, err)
	failures.log(s.logger)
	return Secret{
		ID:             row.ID,
		TenantID:       row.TenantID,
		NamespaceID:    row.NamespaceID,
		NamespaceName:  row.NamespaceName,
		Path:           row.Path,
		Description:    row.Description,
		Tags:           nonNilTags(row.Tags),
		CurrentVersion: int(row.CurrentVersion),
		MaskedValue:    masked,
		ExpiresAt:      row.ExpiresAt,
		LastAccessedAt: row.LastAccessedAt,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func secretOpContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, secretOpTimeout)
}

// errorf wraps unexpected errors with context while keeping apperr errors.
func errorf(err error, format string, args ...any) error {
	if _, ok := apperr.As(err); ok {
		return err
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
