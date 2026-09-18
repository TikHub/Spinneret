package identitysvc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
)

// Import modes.
const (
	ImportModeUpsert     = "upsert"
	ImportModeCreateOnly = "create_only"
)

// maxFailureMessageBytes bounds the message of one import failure.
const maxFailureMessageBytes = 1024

// importRow is a validated import row ready to be written.
type importRow struct {
	line    int
	payload preparedPayload
	account string
	region  string
	tags    []string
	labels  map[string]string
}

// failureCollector gathers row failures in line order, keeping at most
// MaxImportFailures entries.
type failureCollector struct {
	items   []ImportFailure
	dropped int
}

func (c *failureCollector) add(line int, msg string) {
	if len(c.items) >= MaxImportFailures {
		c.dropped++
		return
	}
	c.items = append(c.items, ImportFailure{Line: line, Message: truncateText(msg, maxFailureMessageBytes)})
}

func (c *failureCollector) count() int { return len(c.items) + c.dropped }

func (c *failureCollector) result() []ImportFailure {
	out := slices.Clone(c.items)
	// Rows rejected while planning chunks are added after the parse and
	// validation failures; report every failure in line order.
	slices.SortStableFunc(out, func(a, b ImportFailure) int { return a.Line - b.Line })
	if c.dropped > 0 {
		out = append(out, ImportFailure{Line: 0, Message: fmt.Sprintf("%d more rows failed", c.dropped)})
	}
	if out == nil {
		out = []ImportFailure{}
	}
	return out
}

// importState accumulates the committed effects of an import.
type importState struct {
	result      ImportResult
	resetIDs    []string // created or payload-changed identities
	metaIDs     []string // identities with attribute changes only
	accountIDs  []string // accounts created or newly attached
	transitions []transition
	failures    []ImportFailure // rows rejected while planning chunks
}

// ImportIdentities imports identities of one type from JSON Lines or CSV
// (identity:write on the site). Rows referencing secrets (secret_ref fields)
// that the caller may not read or that do not exist are rejected, unless the
// existing identity's stored payload already references them (see
// secretRefAccess). Rows are deduplicated by the keyed hash of
// their unique_by values: new keys create identities (pending for probe
// activation, active for immediate), existing keys receive a new payload
// version when the payload changed ("upsert", the default) and attribute
// updates (account, region, tags, labels that the row sets) otherwise.
// "create_only" leaves existing identities untouched and counts them as
// unchanged. Rows are written in transactions of ImportChunkSize rows, so an
// infrastructure failure may leave earlier chunks committed (they are still
// synchronized and audited). With DryRun everything is validated and counted
// but nothing is written.
func (s *Service) ImportIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in ImportInput) (ImportResult, error) {
	if err := s.checkCrypto(); err != nil {
		return ImportResult{}, err
	}
	site, err := siteByName(ns, in.Site)
	if err != nil {
		return ImportResult{}, err
	}
	if err := requireSite(p, authz.PermIdentityWrite, ns, site); err != nil {
		return ImportResult{}, err
	}
	mode := in.Mode
	if mode == "" {
		mode = ImportModeUpsert
	}
	if mode != ImportModeUpsert && mode != ImportModeCreateOnly {
		return ImportResult{}, invalid("mode %q must be %q or %q", truncateText(in.Mode, 32), ImportModeUpsert, ImportModeCreateOnly)
	}
	if int64(len(in.Data)) > ImportMaxBytes {
		return ImportResult{}, invalid("import exceeds the limit of %d bytes", ImportMaxBytes)
	}
	if in.Type == "" {
		return ImportResult{}, invalid("type is required")
	}
	queries := identitysvcdb.New(s.pool)
	ct, err := s.compiledTypeByName(ctx, queries, site, in.Type)
	if err != nil {
		return ImportResult{}, err
	}
	if ct, err = currentType(ctx, queries, ct); err != nil {
		return ImportResult{}, err
	}
	parsed, rowErrs, err := identity.ParseImport(in.Format, strings.NewReader(in.Data),
		identity.ImportLimits{MaxRows: ImportMaxRows, MaxBytes: ImportMaxBytes})
	if err != nil {
		return ImportResult{}, err
	}
	failures := &failureCollector{}
	rows := s.prepareImportRows(ct, parsed, rowErrs, failures)
	access := newSecretRefAccess(p, ns)
	var refs []string
	for i := range rows {
		refs = append(refs, rows[i].payload.secretRefs...)
	}
	if err := access.check(ctx, queries, dedupe(refs)); err != nil {
		for i := range rows {
			clear(rows[i].payload.canonical)
		}
		return ImportResult{}, err
	}

	st := &importState{}
	var chunkErr error
	for start := 0; start < len(rows) && chunkErr == nil; start += ImportChunkSize {
		chunk := rows[start:min(start+ImportChunkSize, len(rows))]
		chunkErr = s.importChunk(ctx, p, ns, site, ct, mode, in.DryRun, chunk, access, st)
		for i := range chunk {
			clear(chunk[i].payload.canonical)
		}
	}
	for i := range rows {
		clear(rows[i].payload.canonical)
	}
	for _, f := range st.failures {
		failures.add(f.Line, f.Message)
	}
	st.result.Failed = failures.result()
	if in.DryRun {
		if chunkErr != nil {
			return ImportResult{}, chunkErr
		}
		return st.result, nil
	}
	s.finishImport(ctx, p, ns, site, ct, in, mode, st, failures.count(), chunkErr)
	if chunkErr != nil {
		return ImportResult{}, chunkErr
	}
	return st.result, nil
}

// prepareImportRows validates and normalizes parsed rows. Parse errors and
// row problems are recorded in line order; rows whose unique key repeats an
// earlier row are rejected.
func (s *Service) prepareImportRows(ct *identity.CompiledType, parsed []identity.ImportRow, rowErrs []identity.RowError,
	failures *failureCollector) []importRow {
	out := make([]importRow, 0, len(parsed))
	firstLine := make(map[string]int, len(parsed))
	next := 0
	flush := func(line int) {
		for next < len(rowErrs) && (line < 0 || rowErrs[next].Line < line) {
			failures.add(rowErrs[next].Line, rowErrs[next].Message)
			next++
		}
	}
	for i := range parsed {
		raw := &parsed[i]
		flush(raw.Line)
		row, err := s.prepareImportRow(ct, raw)
		raw.Payload = nil
		if err != nil {
			failures.add(raw.Line, errorMessage(err))
			continue
		}
		key := string(row.payload.uniqueHash)
		if line, dup := firstLine[key]; dup {
			clear(row.payload.canonical)
			failures.add(raw.Line, fmt.Sprintf("duplicate unique key (same identity as line %d)", line))
			continue
		}
		firstLine[key] = raw.Line
		out = append(out, row)
	}
	flush(-1)
	return out
}

// prepareImportRow validates one parsed row.
func (s *Service) prepareImportRow(ct *identity.CompiledType, raw *identity.ImportRow) (importRow, error) {
	if err := validateAccountRef(raw.Account); err != nil {
		return importRow{}, err
	}
	if err := validateText("region", raw.Region, MaxRegionBytes); err != nil {
		return importRow{}, err
	}
	tags, err := validateTags(raw.Tags)
	if err != nil {
		return importRow{}, err
	}
	if err := validateLabels(raw.Labels); err != nil {
		return importRow{}, err
	}
	prep, err := s.preparePayload(ct, raw.Payload)
	if err != nil {
		return importRow{}, err
	}
	return importRow{line: raw.Line, payload: prep, account: raw.Account, region: raw.Region, tags: tags, labels: raw.Labels}, nil
}

// finishImport synchronizes the hot state, publishes state events and writes
// the audit entry of a (possibly partially) committed import.
func (s *Service) finishImport(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site *catalog.Site,
	ct *identity.CompiledType, in ImportInput, mode string, st *importState, failed int, importErr error) {
	synced := s.syncHot(ctx, site.ID, []syncGroup{
		{ids: st.resetIDs, opts: SyncOptions{ResetHealth: true, ResetFailures: true}},
		{ids: st.metaIDs},
	}, st.accountIDs)
	s.publishTransitions(ctx, ns, site.ID, st.transitions)
	details := map[string]any{
		"site": site.Name, "type": ct.Name, "format": in.Format, "mode": mode,
		"created": st.result.Created, "updated": st.result.Updated, "unchanged": st.result.Unchanged, "failed": failed,
	}
	if !synced {
		details["hot_sync_failed"] = true
	}
	result := audit.ResultOK
	if importErr != nil {
		result = audit.ResultError
		if ae, ok := apperr.As(importErr); ok {
			details["error"] = string(ae.Reason)
		} else {
			details["error"] = string(apperr.ReasonInternal)
		}
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity.import", "identity_type", ct.ID, ct.Name,
		result, details))
}
