package identitysvc

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
)

// MaxCountedRows caps the totals of identity lists.
const MaxCountedRows = 1_000_000

// identityColumns are the columns scanned into identitysvcdb.IdentityGetRow.
const identityColumns = `i.id, i.site_id, i.client, i.type_id, t.name, i.account_id, coalesce(a.external_ref, ''),
       i.state, i.state_reason, i.state_changed_at, i.ban_until, i.quarantine_until, i.region, i.tags, i.labels,
       i.payload_version, i.activated_at, i.last_used_at,
       coalesce(h.score, 70)::double precision, coalesce(h.samples, 0)::integer, coalesce(pb.proxy_id, ''),
       i.created_at, i.updated_at`

const (
	joinTypes    = ` JOIN identity_types t ON t.id = i.type_id`
	joinAccounts = ` LEFT JOIN accounts a ON a.id = i.account_id`
	joinScores   = ` LEFT JOIN hot_state_snapshots h ON h.site_id = i.site_id AND h.subject = 'ig'` +
		` AND h.subject_id = i.id AND h.endpoint_group_id = ''`
	joinBindings = ` LEFT JOIN proxy_bindings pb ON pb.identity_id = i.id`
)

// scoreExpr is the approximate global score of an identity: the last
// hot-state snapshot, or the default baseline when there is none.
const scoreExpr = `coalesce(h.score, 70)`

// epoch is the sort value used for identities that were never used.
var epoch = time.Unix(0, 0).UTC()

// orderExprs whitelists the sort expressions of ListIdentities.
var orderExprs = map[string]string{
	OrderCreatedAt:      "i.created_at",
	OrderUpdatedAt:      "i.updated_at",
	OrderStateChangedAt: "i.state_changed_at",
	OrderLastUsedAt:     "coalesce(i.last_used_at, 'epoch'::timestamptz)",
	OrderScore:          scoreExpr,
}

// identityCursor is the keyset position of ListIdentities.
type identityCursor struct {
	Order string     `json:"o"`
	Desc  bool       `json:"d"`
	Time  *time.Time `json:"t,omitempty"`
	Score *float64   `json:"s,omitempty"`
	ID    string     `json:"i"`
}

// sqlArgs accumulates positional query arguments.
type sqlArgs []any

func (a *sqlArgs) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// filterSQL builds the WHERE conditions of an identity filter over the
// visible site IDs and reports which optional joins they need.
func filterSQL(f IdentityFilter, sites []string, args *sqlArgs) (conds []string, needTypes, needAccounts, needScores bool) {
	conds = append(conds, "i.site_id = ANY("+args.add(sites)+"::text[])")
	if f.Type != "" {
		conds = append(conds, "t.name = "+args.add(f.Type))
		needTypes = true
	}
	switch {
	case len(f.States) > 0:
		conds = append(conds, "i.state = ANY("+args.add(f.States)+"::text[])")
	case !f.IncludeRetired:
		conds = append(conds, "i.state <> 'retired'")
	}
	if len(f.Tags) > 0 {
		conds = append(conds, "i.tags @> "+args.add(f.Tags)+"::text[]")
	}
	if f.AccountRef != "" {
		conds = append(conds, "a.external_ref = "+args.add(f.AccountRef))
		needAccounts = true
	}
	if f.Region != "" {
		conds = append(conds, "i.region = "+args.add(f.Region))
	}
	if f.Search != "" {
		ph := args.add(f.Search)
		conds = append(conds, "(starts_with(i.id, "+ph+") OR EXISTS (SELECT 1 FROM jsonb_each_text(i.labels) l WHERE l.value = "+ph+"))")
	}
	if f.MinScore != nil {
		conds = append(conds, scoreExpr+" >= "+args.add(*f.MinScore))
		needScores = true
	}
	if f.MaxScore != nil {
		conds = append(conds, scoreExpr+" <= "+args.add(*f.MaxScore))
		needScores = true
	}
	return conds, needTypes, needAccounts, needScores
}

// listIdentitiesSQL builds the page query of ListIdentities. It selects
// limit+1 rows so the caller can detect a next page.
func listIdentitiesSQL(f IdentityFilter, sites []string, orderBy string, desc bool, after *identityCursor, limit int) (string, []any) {
	var args sqlArgs
	conds, _, _, _ := filterSQL(f, sites, &args)
	expr := orderExprs[orderBy]
	dir, cmp := "ASC", ">"
	if desc {
		dir, cmp = "DESC", "<"
	}
	if after != nil {
		var v any
		if orderBy == OrderScore {
			v = *after.Score
		} else {
			v = *after.Time
		}
		conds = append(conds, "("+expr+", i.id) "+cmp+" ("+args.add(v)+", "+args.add(after.ID)+")")
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(identityColumns)
	b.WriteString(" FROM identities i")
	b.WriteString(joinTypes + joinAccounts + joinScores + joinBindings)
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(conds, " AND "))
	b.WriteString(" ORDER BY " + expr + " " + dir + ", i.id " + dir)
	b.WriteString(" LIMIT " + args.add(limit+1))
	return b.String(), args
}

// countIdentitiesSQL builds the capped count query of an identity filter.
func countIdentitiesSQL(f IdentityFilter, sites []string) (string, []any) {
	var args sqlArgs
	conds, needTypes, needAccounts, needScores := filterSQL(f, sites, &args)
	var b strings.Builder
	b.WriteString("SELECT count(*) FROM (SELECT 1 FROM identities i")
	if needTypes {
		b.WriteString(joinTypes)
	}
	if needAccounts {
		b.WriteString(joinAccounts)
	}
	if needScores {
		b.WriteString(joinScores)
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(conds, " AND "))
	b.WriteString(" LIMIT " + args.add(MaxCountedRows) + ") c")
	return b.String(), args
}

// identityIDsSQL builds a keyset query returning the IDs of matching
// identities in ID order after afterID.
func identityIDsSQL(f IdentityFilter, sites []string, afterID string, limit int) (string, []any) {
	var args sqlArgs
	conds, needTypes, needAccounts, needScores := filterSQL(f, sites, &args)
	conds = append(conds, "i.id > "+args.add(afterID))
	var b strings.Builder
	b.WriteString("SELECT i.id FROM identities i")
	if needTypes {
		b.WriteString(joinTypes)
	}
	if needAccounts {
		b.WriteString(joinAccounts)
	}
	if needScores {
		b.WriteString(joinScores)
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(conds, " AND "))
	b.WriteString(" ORDER BY i.id LIMIT " + args.add(limit))
	return b.String(), args
}

// cursorFor returns the keyset position after row.
func cursorFor(row identitysvcdb.IdentityGetRow, orderBy string, desc bool) identityCursor {
	c := identityCursor{Order: orderBy, Desc: desc, ID: row.ID}
	var t time.Time
	switch orderBy {
	case OrderScore:
		score := row.GlobalScore
		c.Score = &score
		return c
	case OrderUpdatedAt:
		t = row.UpdatedAt
	case OrderStateChangedAt:
		t = row.StateChangedAt
	case OrderLastUsedAt:
		t = epoch
		if row.LastUsedAt != nil {
			t = *row.LastUsedAt
		}
	default:
		t = row.CreatedAt
	}
	t = t.UTC()
	c.Time = &t
	return c
}

// validateFilter checks an identity filter.
func validateFilter(f *IdentityFilter) error {
	for _, st := range f.States {
		if !slices.Contains(identityStates, st) {
			return invalid("state %q must be one of %s", truncateText(st, 32), strings.Join(identityStates, ", "))
		}
	}
	f.States = dedupe(f.States)
	tags, err := validateTags(f.Tags)
	if err != nil {
		return err
	}
	f.Tags = tags
	checks := []struct {
		field string
		value string
		max   int
	}{
		{"type", f.Type, 64}, {"account_ref", f.AccountRef, MaxAccountRefBytes},
		{"region", f.Region, MaxRegionBytes}, {"search", f.Search, 256},
	}
	for _, c := range checks {
		if err := validateText(c.field, c.value, c.max); err != nil {
			return err
		}
	}
	for _, sc := range []*float64{f.MinScore, f.MaxScore} {
		if sc != nil && (math.IsNaN(*sc) || *sc < 0 || *sc > 100) {
			return invalid("score bounds must be between 0 and 100")
		}
	}
	if f.MinScore != nil && f.MaxScore != nil && *f.MinScore > *f.MaxScore {
		return invalid("min_score must not exceed max_score")
	}
	return nil
}
